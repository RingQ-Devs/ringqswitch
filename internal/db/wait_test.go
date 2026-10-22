package db

import "testing"

// TestWaitSeconds: a queue call waited in the queue (not 0 because the
// queue answered it at once); others waited until bridged or answered, and
// a missed call for as long as it rang.
func TestWaitSeconds(t *testing.T) {
	for _, c := range []struct {
		name     string
		v        map[string]string
		answered bool
		want     string
	}{
		{"queue answered", map[string]string{"start_epoch": "100", "answer_epoch": "100", "cc_queue_joined_epoch": "103", "cc_queue_answered_epoch": "125", "end_epoch": "200"}, true, "22"},
		{"queue abandoned", map[string]string{"start_epoch": "100", "answer_epoch": "100", "cc_queue_joined_epoch": "101", "cc_queue_canceled_epoch": "141", "end_epoch": "141"}, true, "40"},
		{"bridged", map[string]string{"start_epoch": "100", "answer_epoch": "100", "bridge_epoch": "107", "end_epoch": "160"}, true, "7"},
		{"answered", map[string]string{"start_epoch": "100", "answer_epoch": "104", "end_epoch": "160"}, true, "4"},
		{"missed", map[string]string{"start_epoch": "100", "end_epoch": "118"}, false, "18"},
		{"no times", map[string]string{}, false, ""},
	} {
		if got := waitSeconds(c.v, c.answered); got != c.want {
			t.Errorf("%s: wait %q, want %q", c.name, got, c.want)
		}
	}
}
