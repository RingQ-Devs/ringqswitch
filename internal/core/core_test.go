package core

import (
	"testing"
)

func TestParseDialString(t *testing.T) {
	ds, err := parseDialString("{call_timeout=20,origination_caller_id_name='Sales, Inc'}user/1000,[leg_timeout=5]user/1001|gateway/carrier/15551234")
	if err != nil {
		t.Fatal(err)
	}
	if ds.vars["call_timeout"] != "20" || ds.vars["origination_caller_id_name"] != "Sales, Inc" {
		t.Fatalf("global vars %v", ds.vars)
	}
	if len(ds.groups) != 2 || len(ds.groups[0]) != 2 || len(ds.groups[1]) != 1 {
		t.Fatalf("groups %+v", ds.groups)
	}
	if ds.groups[0][1].target != "user/1001" || ds.groups[0][1].vars["leg_timeout"] != "5" {
		t.Fatalf("leg vars %+v", ds.groups[0][1])
	}
	if ds.groups[1][0].target != "gateway/carrier/15551234" {
		t.Fatalf("sequential group %+v", ds.groups[1])
	}
	for _, bad := range []string{"", "  ", "{a=b", "[x=y"} {
		if _, err := parseDialString(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestCauses(t *testing.T) {
	cases := []struct {
		sip   int
		cause Cause
		back  int
	}{
		{486, CauseUserBusy, 486},
		{404, CauseUnallocatedNumber, 404},
		{603, CauseCallRejected, 603},
		{487, CauseOriginatorCancel, 487},
		{503, CauseNormalTemporaryFailure, 503},
		{488, CauseIncompatibleDestination, 488},
	}
	for _, c := range cases {
		if got := CauseFromSIP(c.sip); got != c.cause {
			t.Errorf("CauseFromSIP(%d) = %s, want %s", c.sip, got, c.cause)
		}
		if got := c.cause.SIPCode(); got != c.back {
			t.Errorf("%s.SIPCode() = %d, want %d", c.cause, got, c.back)
		}
	}
	if ParseCause("user_busy") != CauseUserBusy || ParseCause("17") != CauseUserBusy || ParseCause("") != CauseNormalClearing {
		t.Fatal("ParseCause")
	}
	if CauseUserNotRegistered.Q850() != 20 || CauseLoseRace.Q850() != 16 {
		t.Fatal("non Q.850 causes must map to standard values in Reason headers")
	}
	if !continueOnFail("USER_BUSY,NO_ANSWER", CauseNoAnswer) || continueOnFail("USER_BUSY", CauseNoAnswer) || !continueOnFail("true", CauseNoAnswer) {
		t.Fatal("continue_on_fail parsing")
	}
}

func TestPickCause(t *testing.T) {
	if pickCause(nil) != CauseNoRouteDestination {
		t.Fatal("empty")
	}
	if pickCause([]Cause{CauseUserNotRegistered, CauseUserBusy, CauseNoAnswer}) != CauseUserBusy {
		t.Fatal("busy should win")
	}
	if pickCause([]Cause{CauseUserNotRegistered, CauseDestinationOutOfOrder}) != CauseDestinationOutOfOrder {
		t.Fatal("last cause fallback")
	}
}

func TestSplitArgs(t *testing.T) {
	cases := map[string][]string{
		`originate user/1000 1001 XML default Reception 200`:                                {"originate", "user/1000", "1001", "XML", "default", "Reception", "200"},
		`originate user/1000 1001 XML default "Front Desk" 200`:                             {"originate", "user/1000", "1001", "XML", "default", "Front Desk", "200"},
		`originate {origination_caller_id_name='Front Desk',call_timeout=20}user/1000 9197`: {"originate", "{origination_caller_id_name='Front Desk',call_timeout=20}user/1000", "9197"},
		`originate [x='a b']user/1,user/2 &playback(/tmp/a b.wav)`:                          {"originate", "[x='a b']user/1,user/2", "&playback(/tmp/a", "b.wav)"},
		`  show   channels  as json `:                                                       {"show", "channels", "as", "json"},
		`log ""`:                                                                            {"log", ""},
	}
	for in, want := range cases {
		got := splitArgs(in)
		if len(got) != len(want) {
			t.Errorf("%q: got %q want %q", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: got %q want %q", in, got, want)
				break
			}
		}
	}
	ds, err := parseDialString("{origination_caller_id_name='Front Desk',call_timeout=20}user/1000")
	if err != nil || ds.vars["origination_caller_id_name"] != "Front Desk" {
		t.Fatalf("dial string vars: %v %v", ds, err)
	}
}
