package web

import (
	"regexp"
	"strconv"
	"testing"
)

func TestRangeRegex(t *testing.T) {
	pairs := [][2]int{{5, 10}, {0, 9}, {1, 1}, {100, 199}, {1000, 1099}, {7, 1234}, {95, 105}, {1, 9999}, {2001, 2999}, {19, 9001}}
	for _, p := range pairs {
		re := regexp.MustCompile("^(?:" + rangeRegex(p[0], p[1]) + ")$")
		for n := 0; n <= 12000; n++ {
			want := n >= p[0] && n <= p[1]
			if got := re.MatchString(strconv.Itoa(n)); got != want {
				t.Fatalf("range %d-%d (%s): %d matched=%v", p[0], p[1], re, n, got)
			}
		}
	}
}

func TestRulePatternAndChecks(t *testing.T) {
	cases := []struct {
		prefix, length string
		yes, no        []string
	}{
		{"9", "8", []string{"91234567", "+91234567"}, []string{"81234567", "9123456", "912345678"}},
		{"+65", "10", []string{"+6591234567"}, []string{"6591234567", "+659123456"}},
		{"", "7-15", []string{"1234567", "+123456789012345"}, []string{"123456", "1234567890123456"}},
		{"00", "", []string{"0044207946", "00"}, []string{"0144"}},
		{"6", "8,10", []string{"61234567", "6123456789"}, []string{"612345678"}},
	}
	for _, c := range cases {
		p, err := rulePattern(c.prefix, c.length)
		if err != nil {
			t.Fatal(err)
		}
		re := regexp.MustCompile(p)
		for _, n := range c.yes {
			if !re.MatchString(n) {
				t.Errorf("prefix %q length %q (%s) should match %s", c.prefix, c.length, p, n)
			}
		}
		for _, n := range c.no {
			if re.MatchString(n) {
				t.Errorf("prefix %q length %q (%s) should not match %s", c.prefix, c.length, p, n)
			}
		}
	}
	row := map[string]string{"route_name": "Local calls", "route_prefix": "6", "route_length": "8", "route_from_ranges": "100-199 1000-1010",
		"route_from_extensions": "2001", "gateway_uuids": "", "route_block": "false"}
	if err := prepareRoute(row); err == nil {
		t.Fatal("a rule without a gateway and not BLOCK was accepted")
	}
	row["route_block"] = "true"
	if err := prepareRoute(row); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(callerFilter(row))
	for n, want := range map[string]bool{"150": true, "1005": true, "2001": true, "200": false, "1011": false, "99": false} {
		if re.MatchString(n) != want {
			t.Errorf("caller filter %s: %s should be %v", re, n, want)
		}
	}
	if err := prepareRoute(map[string]string{"route_name": " ", "route_pattern": "^1$", "route_block": "true"}); err == nil {
		t.Error("a rule without a name was accepted")
	}
}
