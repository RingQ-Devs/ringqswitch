package core

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

func TestGuardJails(t *testing.T) {
	cfg := config.DefaultGuard(config.Settings{AuthFailLimit: 3, AuthFailWindow: time.Minute, AuthBlockTime: time.Hour})
	g := newGuard(cfg)
	var notes []guardNote
	g.notify = func(n []guardNote) { notes = append(notes, n...) }
	ip := netip.MustParseAddr("203.0.113.9")
	for i := 1; i <= 3; i++ {
		banned, n := g.fail(config.JailSIPPassword, ip, "wrong password")
		if n != i || banned != (i == 3) {
			t.Fatalf("failure %d: banned %v, counted %d", i, banned, n)
		}
	}
	if b := g.banned(ip, "sip"); b == nil || b.Jail != config.JailSIPPassword || b.Failures != 3 || time.Until(b.Until) < 59*time.Minute {
		t.Fatalf("sip ban: %+v", b)
	}
	if g.banned(ip, "web") != nil {
		t.Fatal("a SIP ban closes the web interface too")
	}
	if len(notes) != 1 || notes[0].action != "ban" {
		t.Fatalf("notes: %+v", notes)
	}
	// A disabled protection never bans.
	for i := 0; i < 10; i++ {
		if banned, _ := g.fail(config.JailSIPInvite, ip, "calls"); banned {
			t.Fatal("disabled protection banned")
		}
	}
	// By hand, then lifted.
	g.ban(ip, config.JailWebLogin, time.Hour, "test", "manual", "admin")
	if b := g.banned(ip, "web"); b == nil || b.Source != "manual" {
		t.Fatalf("web ban: %+v", b)
	}
	if len(g.list()) != 2 || len(g.addresses()) != 1 {
		t.Fatalf("list: %+v", g.list())
	}
	if !g.unban(ip, config.JailWebLogin, "admin") || g.banned(ip, "web") != nil || g.banned(ip, "sip") == nil {
		t.Fatal("unban of one protection")
	}
	// Bans end on their own.
	g.ban(netip.MustParseAddr("198.51.100.1"), config.JailWebRequest, -time.Second, "old", "manual", "admin")
	g.sweep()
	if notes[len(notes)-1].action != "expire" {
		t.Fatalf("expiry: %+v", notes[len(notes)-1])
	}
	// Turning SIP Guard off lifts every ban.
	cfg.Enabled = false
	g.configure(cfg)
	if len(g.list()) != 0 || g.banned(ip, "sip") != nil {
		t.Fatal("bans kept with SIP Guard off")
	}
	if banned, _ := g.fail(config.JailSIPPassword, ip, "x"); banned {
		t.Fatal("banned while off")
	}
	// Restored bans (from the database) are in force again.
	cfg.Enabled = true
	g.configure(cfg)
	g.restore([]config.StoredBan{{IP: ip, Jail: config.JailSIPRequest, Since: time.Now(), Until: time.Now().Add(time.Hour)}})
	if b := g.banned(ip, "sip"); b == nil || b.Jail != config.JailSIPRequest {
		t.Fatalf("restored: %+v", b)
	}
}

func TestGuardSlots(t *testing.T) {
	var s guardSlots
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		s.add(now, time.Minute)
	}
	if n := s.add(now.Add(50*time.Second), time.Minute); n != 6 {
		t.Fatalf("within the window: %d", n)
	}
	if n := s.add(now.Add(2*time.Minute), time.Minute); n != 1 {
		t.Fatalf("after the window: %d", n)
	}
	if !s.stale(now.Add(5*time.Minute), time.Minute) {
		t.Fatal("stale")
	}
}

func TestTrunkNumbers(t *testing.T) {
	got := trunkNumbers("{ignore_early_media=true}[x=1]sofia/gateway/abc/0065123,user/1001|ringq/gateway/b2/+44123@carrier")
	if len(got) != 2 || got[0] != "0065123" || got[1] != "+44123" {
		t.Fatalf("numbers: %v", got)
	}
	if n := trunkNumbers("user/1001@d"); len(n) != 0 {
		t.Fatalf("no trunk: %v", n)
	}
}
