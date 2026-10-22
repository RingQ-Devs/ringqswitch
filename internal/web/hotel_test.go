package web_test

import (
	"strings"
	"testing"
	"time"
)

// TestHotelFrontDesk: a guest checks in to a room (an extension), the guest's
// name can change, a wake-up call rings the room and counts as answered, an
// unanswered one is tried again later, and checking out ends the stay and
// its wake-up calls.
func TestHotelFrontDesk(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/hotel"
	room := func(ext string) map[string]any {
		t.Helper()
		for _, x := range r.mustCall("GET", d, nil)["rooms"].([]any) {
			if m := x.(map[string]any); m["extension"] == ext {
				return m
			}
		}
		t.Fatalf("room %s not listed", ext)
		return nil
	}
	if s := room("1001")["status"]; s != "vacant" {
		t.Fatalf("new room: %v", s)
	}
	stay := r.mustCall("POST", d+"/checkin", map[string]string{"extension": "1001", "first_name": "Richard", "last_name": "Miles"})["stay_uuid"].(string)
	var out map[string]any
	if c := r.call("POST", d+"/checkin", map[string]string{"extension": "1001", "first_name": "Ann"}, &out); c != 400 || !strings.Contains(out["error"].(string), "occupied") {
		t.Fatalf("second check-in to an occupied room: %d %v", c, out)
	}
	r.mustCall("PUT", d+"/stays/"+stay, map[string]string{"first_name": "Richard", "last_name": "Miles-Brown"})
	g := room("1001")
	if g["status"] != "occupied" || g["guest"].(map[string]any)["last_name"] != "Miles-Brown" {
		t.Fatalf("occupied room: %v", g)
	}
	if c := r.call("POST", d+"/wakeups", map[string]string{"extension": "1002", "at": time.Now().Format("2006-01-02T15:04")}, &out); c != 400 {
		t.Fatalf("wake-up call for an empty room: %d", c)
	}

	// The guest's phone answers the wake-up call.
	phone := r.phone("1001")
	r.mustCall("POST", d+"/wakeups", map[string]string{"extension": "1001", "at": time.Now().Format("2006-01-02T15:04")})
	r.srv.WakeupNow()
	in, err := phone.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatalf("the room did not ring: %v", err)
	}
	if !strings.Contains(in.Invite.Get("From"), "Wake-up call") {
		t.Fatalf("wake-up call caller ID: %s", in.Invite.Get("From"))
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	waitWakeup := func(ext, status string) map[string]any {
		t.Helper()
		var w map[string]any
		for i := 0; i < 100; i++ {
			if w, _ = room(ext)["wakeup"].(map[string]any); w != nil && w["status"] == status {
				return w
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("wake-up call of %s: %v, want %s", ext, w, status)
		return nil
	}
	waitWakeup("1001", "answered")
	_, _ = in.Dialog().Bye()

	// Nobody answers in room 1000: it is tried again in five minutes.
	r.mustCall("POST", d+"/checkin", map[string]string{"extension": "1000", "first_name": "Sam"})
	r.mustCall("POST", d+"/wakeups", map[string]string{"extension": "1000", "at": time.Now().Format("2006-01-02T15:04")})
	r.srv.WakeupNow()
	w := waitWakeup("1000", "scheduled")
	for i := 0; i < 100 && w["attempts"] != "1"; i++ {
		time.Sleep(50 * time.Millisecond)
		w = room("1000")["wakeup"].(map[string]any)
	}
	at, _ := time.Parse(time.RFC3339, w["call_at"].(string))
	if w["attempts"] != "1" || time.Until(at) < 4*time.Minute || w["last_result"] == "" {
		t.Fatalf("retry of an unanswered wake-up call: %v", w)
	}

	// Checking out cancels the wake-up call and frees the room.
	stay3 := room("1000")["guest"].(map[string]any)["stay_uuid"].(string)
	r.mustCall("POST", d+"/stays/"+stay3+"/checkout", nil)
	if x := room("1000"); x["status"] != "vacant" || x["wakeup"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("after check-out: %v", x)
	}
}
