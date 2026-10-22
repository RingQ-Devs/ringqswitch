package core

import "testing"

func TestDescribeDial(t *testing.T) {
	for dial, want := range map[string]string{
		"user/1001@example.com": "1001",
		"{ignore_early_media=true}[leg_timeout=20]user/1001@a,[x=y]user/1002@a": "1001, 1002",
		"user/1001@a|user/1001@a:_:ringq/gateway/Carrier/6591234567":            "1001, 6591234567 (trunk Carrier)",
		"sofia/gateway/abc-uuid/0065912@x":                                      "0065912 (trunk Main)",
		"error/user_not_registered":                                             "",
		"loopback/3000/default":                                                 "default",
	} {
		if got := describeDial(dial, func(gw string) string {
			return map[string]string{"abc-uuid": "Main"}[gw] + map[bool]string{true: gw}[gw != "abc-uuid"]
		}); got != want {
			t.Errorf("%s: %q, want %q", dial, got, want)
		}
	}
}
