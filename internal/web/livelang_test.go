package web

import "testing"

// TestLiveLang: set to Detect automatically, a call's language is the one
// a clear sentence is heard in, kept for the rest of the call; guesses on
// short or unclear phrases are not shown and do not decide.
func TestLiveLang(t *testing.T) {
	if l := newLiveLang("ta"); l.get() != "ta" || !l.heard("fr", 0.9, 3) || l.get() != "ta" {
		t.Fatalf("a set language stays: %q", l.get())
	}
	l := newLiveLang("auto")
	if l.get() != "auto" {
		t.Fatalf("unknown at first: %q", l.get())
	}
	if l.heard("fr", 0.42, 2.5) || l.get() != "auto" { // noise "heard" as French
		t.Fatalf("an unsure guess was shown or decided: %q", l.get())
	}
	if !l.heard("en", 0.62, 0.8) || l.get() != "auto" { // "Hello?": shown, not decisive
		t.Fatalf("a short phrase decided: %q", l.get())
	}
	if !l.heard("en", 0.91, 1.6) || l.get() != "en" {
		t.Fatalf("a clear sentence did not decide: %q", l.get())
	}
	if !l.heard("fr", 0.95, 4) || l.get() != "en" {
		t.Fatalf("the call's language changed: %q", l.get())
	}
	// Weaker detections add up.
	l = newLiveLang("")
	for i := 0; i < 3; i++ {
		l.heard("ms", 0.7, 1.5)
	}
	if l.get() != "ms" {
		t.Fatalf("detections did not add up: %q", l.get())
	}
}
