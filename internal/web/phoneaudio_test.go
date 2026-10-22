package web

import (
	"strings"
	"testing"

	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

func TestFlowLevels(t *testing.T) {
	var f flow
	f.mark()
	loud := make([]byte, 160)
	for i := range loud {
		loud[i] = media.LinearToUlaw(8000)
	}
	f.add(loud)
	st := f.stats()
	if st.Packets != 1 || st.Loudest < -15 || st.Loudest > -10 {
		t.Fatalf("a -12 dBFS packet: %+v", st)
	}
	silence := make([]byte, 160) // 0xff is PCMU silence
	for i := range silence {
		silence[i] = 0xff
	}
	var s flow
	s.mark()
	s.add(silence)
	if got := s.stats().Loudest; got > silentDB {
		t.Fatalf("PCMU silence should be below %d dBFS, got %v", silentDB, got)
	}
}

func TestAudioDiagnosis(t *testing.T) {
	ok := flowStats{Packets: 500, Level: -30, Recent: -25, Loudest: -20}
	none := flowStats{Level: -90, Recent: -90, Loudest: -90}
	silent := flowStats{Packets: 500, Level: -90, Recent: -90, Loudest: -90}
	cases := []struct {
		name string
		r    audioReport
		want string
	}{
		{"fine", audioReport{FromBrowser: ok, ToCall: ok, FromCall: ok, ToBrowser: ok, Seconds: 10}, ""},
		{"too early", audioReport{FromBrowser: none, ToCall: none, FromCall: none, ToBrowser: none, Seconds: 2}, ""},
		{"carrier RTP blocked", audioReport{FromBrowser: ok, ToCall: ok, FromCall: none, ToBrowser: none, Seconds: 10}, "No sound reaches the server from the other side"},
		{"browser mic silent", audioReport{FromBrowser: silent, ToCall: silent, FromCall: ok, ToBrowser: ok, Seconds: 10}, "only silence"},
		{"browser not sending", audioReport{FromBrowser: none, ToCall: none, FromCall: ok, ToBrowser: ok, Seconds: 10}, "from this browser"},
	}
	for _, c := range cases {
		got := strings.Join(c.r.diagnose(), " ")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
