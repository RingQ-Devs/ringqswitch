package core_test

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/fax"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// faxPBM writes a short test page (a PBM the fax engine loads).
func faxPBM(t *testing.T, path string) {
	t.Helper()
	const w, h = 1728, 260
	var b bytes.Buffer
	fmt.Fprintf(&b, "P4\n%d %d\n", w, h)
	for y := 0; y < h; y++ {
		row := make([]byte, w/8)
		for x := 0; x < w; x++ {
			if (x/40+y/20)%2 == 0 && y > 60 {
				row[x/8] |= 0x80 >> uint(x%8)
			}
		}
		b.Write(row)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// faxPhone runs a fax terminal on a test phone's RTP, in real time.
func faxPhone(t *testing.T, conn *net.UDPConn, remote netip.AddrPort, term *fax.Terminal, limit time.Duration) {
	t.Helper()
	frames := make(chan []int16, 64)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buf := make([]byte, 1500)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if err != nil || n < 12 || buf[1]&0x7F != 0 {
				continue
			}
			pcm := make([]int16, n-12)
			for i, u := range buf[12:n] {
				pcm[i] = media.UlawToLinear(u)
			}
			select {
			case frames <- pcm:
			case <-stop:
				return
			}
		}
	}()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(limit)
	out := make([]int16, 160)
	payload := make([]byte, 160)
	for seq := 0; !term.Done(); seq++ {
		if time.Now().After(deadline) {
			t.Fatalf("fax phone: no result after %v", limit)
		}
		<-tick.C
	drain:
		for {
			select {
			case f := <-frames:
				term.Receive(f)
			default:
				break drain
			}
		}
		term.Transmit(out)
		for i, v := range out {
			payload[i] = media.LinearToUlaw(v)
		}
		_ = testua.SendRTP(conn, remote, 0, uint16(seq), uint32(seq*160), payload)
	}
}

func TestFaxOverSIP(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time fax calls take about a minute")
	}
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox", "fax.pdf")
	doc := filepath.Join(dir, "doc.pbm")
	faxPBM(t, doc)
	dp := `<include><context name="default">
  <extension name="fax in"><condition field="destination_number" expression="^3290$">
    <action application="set" data="fax_ident=+6560000001"/>
    <action application="answer"/>
    <action application="rxfax" data="` + inbox + `"/>
    <action application="hangup"/>
  </condition></extension>
  <extension name="local"><condition field="destination_number" expression="^(10\d\d)$">
    <action application="bridge" data="user/$1"/>
  </condition></extension>
</context></include>`
	users := "<include>"
	for _, u := range []string{"1000", "1001"} {
		users += fmt.Sprintf(`<user id="%s"><params><param name="password" value="%s"/></params></user>`, u, testPassword)
	}
	users += "</include>"
	h := newHarnessWith(t, "", users, dp, nil)
	sub := h.sw.Events().Subscribe(64, func(e *core.Event) bool { return strings.HasPrefix(e.Subclass(), "spandsp::") })
	defer sub.Close()

	t.Run("phone sends, rxfax receives", func(t *testing.T) {
		alice := h.phone("1000")
		call, err := alice.Dial("3290", []int{0}, 5*time.Second, false)
		if err != nil || call.Final.StatusCode != 200 {
			t.Fatalf("fax call failed: %v", err)
		}
		pages, err := fax.LoadDocument(doc)
		if err != nil {
			t.Fatal(err)
		}
		tx := fax.NewSender(pages, fax.Options{Ident: "+6561234567", Header: "TEST CO"})
		faxPhone(t, call.RTP, call.RemoteRTP, tx, 60*time.Second)
		if r := tx.Result(); !r.Success || r.RemoteID != "+6560000001" {
			t.Fatalf("sender: %+v", r)
		}
		e := waitEvent(t, sub, "spandsp::rxfaxresult")
		if e.Get("fax-success") != "1" || e.Get("fax-document-transferred-pages") != "1" ||
			e.Get("fax-remote-station-id") != "+6561234567" || e.Get("variable_fax_success") != "1" {
			t.Fatalf("rxfax result: %s", e.Plain())
		}
		got, err := os.ReadFile(inbox)
		if err != nil || !bytes.HasPrefix(got, []byte("%PDF")) {
			t.Fatalf("inbox file: %v", err)
		}
		h.waitChannels(0)
	})

	t.Run("txfax sends, phone receives", func(t *testing.T) {
		bob := h.phone("1001")
		done := make(chan string, 1)
		go func() {
			done <- h.sw.API("originate {fax_ident=+6560000002,fax_header='RINGQ TEST',origination_caller_id_number=6560000002}user/1001 &txfax(" + doc + ")")
		}()
		in, err := bob.WaitInvite(5 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := in.Answer(0); err != nil {
			t.Fatal(err)
		}
		rx := fax.NewReceiver(fax.Options{Ident: "+6561111111"})
		faxPhone(t, in.RTP, in.Remote, rx, 60*time.Second)
		r := rx.Result()
		if !r.Success || r.Pages != 1 || r.RemoteID != "+6560000002" {
			t.Fatalf("receiver: %+v", r)
		}
		pages, _ := fax.LoadDocument(doc)
		// below the header line, the page is what was sent
		for y := 40; y < len(pages[0].Rows); y++ {
			if !bytes.Equal(r.Received[0].Rows[y], pages[0].Rows[y]) {
				t.Fatalf("row %d differs", y)
			}
		}
		e := waitEvent(t, sub, "spandsp::txfaxresult")
		if e.Get("fax-success") != "1" || e.Get("fax-document-transferred-pages") != "1" {
			t.Fatalf("txfax result: %s", e.Plain())
		}
		if out := <-done; !strings.HasPrefix(out, "+OK") {
			t.Fatalf("originate: %q", out)
		}
	})
}

func waitEvent(t *testing.T, sub *core.Subscription, subclass string) *core.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Subclass() == subclass {
				return e
			}
		case <-timeout:
			t.Fatalf("no %s event", subclass)
		}
	}
}

// buildFaxPeer compiles the spandsp test peer (skips without spandsp).
func buildFaxPeer(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "faxpeer")
	if msg, err := exec.Command("cc", "-O2", "-o", out, "../fax/testdata/faxpeer.c", "-lspandsp").CombinedOutput(); err != nil {
		t.Skipf("no spandsp peer: %v %s", err, msg)
	}
	return out
}

// spandspLine runs a spandsp fax machine on a call's RTP in real time:
// what arrives (A-law or mu-law) goes to it 20 ms at a time, what it sends
// goes back as RTP.
func spandspLine(t *testing.T, peer string, args []string, conn *net.UDPConn, remote netip.AddrPort, pt uint8, limit time.Duration) string {
	t.Helper()
	cmd := exec.Command(peer, args...)
	cmd.Env = append(os.Environ(), "FAXPEER_MODEMS=3", "FAXPEER_IDENT=+6563401007")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill() //nolint:errcheck
	frames := make(chan []int16, 64)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buf := make([]byte, 1500)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if err != nil || n < 12 || buf[1]&0x7F != pt {
				continue
			}
			pcm := make([]int16, n-12)
			for i, b := range buf[12:n] {
				if pt == 8 {
					pcm[i] = media.AlawToLinear(b)
				} else {
					pcm[i] = media.UlawToLinear(b)
				}
			}
			select {
			case frames <- pcm:
			case <-stop:
				return
			}
		}
	}()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(limit)
	in := make([]byte, 320)
	out := make([]byte, 320)
	payload := make([]byte, 160)
	for seq := 0; time.Now().Before(deadline) && !strings.Contains(stderr.String(), "RESULT"); seq++ {
		<-tick.C
		pcm := make([]int16, 160)
		select {
		case f := <-frames:
			copy(pcm, f)
		default:
		}
		for i, v := range pcm {
			in[2*i], in[2*i+1] = byte(v), byte(uint16(v)>>8)
		}
		if _, err := stdin.Write(in); err != nil {
			break
		}
		if _, err := io.ReadFull(stdout, out); err != nil {
			break
		}
		for i := range payload {
			v := int16(uint16(out[2*i]) | uint16(out[2*i+1])<<8)
			if pt == 8 {
				payload[i] = media.LinearToAlaw(v)
			} else {
				payload[i] = media.LinearToUlaw(v)
			}
		}
		_ = testua.SendRTP(conn, remote, pt, uint16(seq), uint32(seq*160), payload)
	}
	stdin.Close()
	return stderr.String()
}

// TestFaxToCarrierPCMA: txfax on a call through a carrier that plays early
// media (183) and answers with A-law only, to a spandsp fax machine.
func TestFaxToCarrierPCMA(t *testing.T) {
	if testing.Short() {
		t.Skip("a real-time fax call")
	}
	peer := buildFaxPeer(t)
	carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	defer carrier.Close()
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.pbm")
	faxPBM(t, doc)
	users := fmt.Sprintf(`<include><user id="1000"><params><param name="password" value="%s"/></params></user></include>`, testPassword)
	h := newHarnessWith(t, "", users, testDialplan, nil)
	carrier.Server = h.addr
	sub := h.sw.Events().Subscribe(64, func(e *core.Event) bool { return e.Subclass() == "spandsp::txfaxresult" })
	defer sub.Close()
	done := make(chan string, 1)
	go func() {
		done <- h.sw.API(fmt.Sprintf("originate {fax_ident=6569142668,fax_header=RINGQ,fax_verbose=true,ignore_early_media=true}sip:63401007@%s &txfax(%s)", carrier.Local, doc))
	}()
	in, err := carrier.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	port := in.RTP.LocalAddr().(*net.UDPAddr).Port
	if err := carrier.Respond(in.Invite, 183, in.ToTag, testua.SDP(port, 8), "application/sdp"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := in.Answer(8); err != nil {
		t.Fatal(err)
	}
	log := spandspLine(t, peer, []string{"receive", filepath.Join(dir, "rx.tif")}, in.RTP, in.Remote, 8, 90*time.Second)
	if !strings.Contains(log, "RESULT 0 ") {
		t.Fatalf("spandsp did not receive the fax: %s", lastLines(log, 15))
	}
	e := waitEvent(t, sub, "spandsp::txfaxresult")
	if e.Get("fax-success") != "1" {
		t.Fatalf("txfax: %s", e.Plain())
	}
	<-done
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// TestFaxFailureKeepsAudio: when the far end never answers as a fax machine,
// the fax fails and the call's audio is kept for troubleshooting.
func TestFaxFailureKeepsAudio(t *testing.T) {
	carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	defer carrier.Close()
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.pbm")
	faxPBM(t, doc)
	users := fmt.Sprintf(`<include><user id="1000"><params><param name="password" value="%s"/></params></user></include>`, testPassword)
	h := newHarnessWith(t, "", users, testDialplan, nil)
	carrier.Server = h.addr
	sub := h.sw.Events().Subscribe(64, func(e *core.Event) bool { return e.Subclass() == "spandsp::txfaxresult" })
	defer sub.Close()
	go h.sw.API(fmt.Sprintf("originate {domain_uuid=a0000000-0000-4000-8000-0000000000aa}sip:63401007@%s &txfax(%s)", carrier.Local, doc))
	in, err := carrier.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.Answer(8); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ { // a voice line: someone says hello (a 400 Hz tone) and hangs up
		_ = testua.SendRTP(in.RTP, in.Remote, 8, uint16(i), uint32(i*160), alawTone(400, i))
		time.Sleep(20 * time.Millisecond)
	}
	_, _ = in.Dialog().Bye()
	e := waitEvent(t, sub, "spandsp::txfaxresult")
	if e.Get("fax-success") != "0" || e.Get("fax-result-text") != "The call dropped prematurely" {
		t.Fatalf("txfax: %s", e.Plain())
	}
	trace := filepath.Join(h.sw.Config().Settings.FaxDir, "a0000000-0000-4000-8000-0000000000aa", "trace", e.Get("Unique-ID")+"-heard.wav")
	for i := 0; i < 50; i++ {
		if st, err := os.Stat(trace); err == nil && st.Size() > 30000 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no call audio kept at %s", trace)
}

func alawTone(hz float64, frame int) []byte {
	b := make([]byte, 160)
	for k := range b {
		n := float64(frame*160 + k)
		b[k] = media.LinearToAlaw(int16(8000 * math.Sin(2*math.Pi*hz*n/8000)))
	}
	return b
}
