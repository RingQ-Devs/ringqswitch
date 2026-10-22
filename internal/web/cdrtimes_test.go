package web

import "testing"

// TestCDRTimes: records of queue calls (also older ones, written with a
// wait of 0) show the time in the queue and the talk with the agent.
func TestCDRTimes(t *testing.T) {
	r := map[string]string{"waitsec": "0", "billsec": "810", "end_epoch": "1910", "cc_queue_joined_epoch": "1100", "cc_queue_answered_epoch": "1130"}
	cdrTimes(r)
	if r["waitsec"] != "30" || r["talksec"] != "780" {
		t.Fatalf("queue call: wait %s talk %s", r["waitsec"], r["talksec"])
	}
	r = map[string]string{"waitsec": "0", "billsec": "40", "end_epoch": "1140", "cc_queue_joined_epoch": "1100", "cc_queue_canceled_epoch": "1140"}
	cdrTimes(r)
	if r["waitsec"] != "40" || r["talksec"] != "0" {
		t.Fatalf("abandoned: wait %s talk %s", r["waitsec"], r["talksec"])
	}
	r = map[string]string{"waitsec": "3", "billsec": "61"}
	cdrTimes(r)
	if r["waitsec"] != "3" || r["talksec"] != "61" {
		t.Fatalf("plain call: %v", r)
	}
}
