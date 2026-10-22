package web

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Outbound rules (RingQ's "New outbound rule"): which calls match (from
// which extensions, to numbers starting with a prefix and of a length),
// where they go (gateways, or BLOCK), how the number is changed (strip,
// prepend), and how the call presents itself (caller ID, SIP From user,
// display name and host, caller ID header type).

var (
	reRulePrefix = regexp.MustCompile(`^\+?[0-9*#]{0,15}$`)
	reRuleLength = regexp.MustCompile(`^\d{1,2}(\s*-\s*\d{1,2})?(\s*,\s*\d{1,2}(\s*-\s*\d{1,2})?)*$`)
	reRuleRange  = regexp.MustCompile(`^(\d{1,9})(?:\s*-\s*(\d{1,9}))?$`)
	reRuleExt    = regexp.MustCompile(`^[0-9A-Za-z*#+._-]{1,32}$`)
)

// Choices of the "Outbound call settings" section.
var (
	ruleFromUser    = map[string]string{"": "", "cid": "${effective_caller_id_number}", "extension": "${user_name}"}
	ruleFromDisplay = map[string]string{"": "", "cid_name": "${effective_caller_id_name}", "cid_number": "${effective_caller_id_number}", "extension": "${caller_id_name}"}
	ruleFromHost    = map[string]bool{"": true, "gateway": true, "domain": true}
	ruleCIDType     = map[string]bool{"": true, "pid": true, "rpid": true, "none": true}
)

// prepareRoute checks an outbound rule and builds its number pattern from
// the prefix and length when they are given.
func prepareRoute(row map[string]string) error {
	name := strings.TrimSpace(row["route_name"])
	if name == "" { // the form asks for 4 characters, like RingQ; older rules may have less
		return badRequest("rule name: required")
	}
	row["route_name"] = name
	prefix := strings.TrimSpace(row["route_prefix"])
	length := strings.Join(strings.Fields(row["route_length"]), "")
	if !reRulePrefix.MatchString(prefix) {
		return badRequest("prefix: digits (and a leading +) only")
	}
	if length != "" && !reRuleLength.MatchString(length) {
		return badRequest("length: a number of digits, e.g. 8, 8,10 or 7-15")
	}
	row["route_prefix"], row["route_length"] = prefix, length
	if prefix != "" || length != "" {
		p, err := rulePattern(prefix, length)
		if err != nil {
			return err
		}
		row["route_pattern"] = p
	}
	if strings.TrimSpace(row["route_pattern"]) == "" {
		return badRequest("give the numbers this rule is for: a prefix, a length, or a pattern")
	}
	if _, err := regexp.Compile(row["route_pattern"]); err != nil {
		return badRequest("pattern: %v", err)
	}
	var exts []string
	for _, e := range strings.Split(row["route_from_extensions"], ",") {
		if e = strings.TrimSpace(e); e != "" {
			if !reRuleExt.MatchString(e) {
				return badRequest("calls from extensions: %q is not an extension", e)
			}
			exts = append(exts, e)
		}
	}
	row["route_from_extensions"] = strings.Join(exts, ",")
	var ranges []string
	for _, r := range strings.FieldsFunc(row["route_from_ranges"], func(c rune) bool { return c == ',' || c == ' ' }) {
		lo, hi, ok := parseRange(r)
		if !ok {
			return badRequest("extension range %q: write it as 100-199", r)
		}
		if hi-lo > 100000 {
			return badRequest("extension range %q is too large", r)
		}
		ranges = append(ranges, strconv.Itoa(lo)+"-"+strconv.Itoa(hi))
	}
	row["route_from_ranges"] = strings.Join(ranges, ",")
	block := truthy(row["route_block"])
	row["route_block"] = strconv.FormatBool(block)
	if !block && strings.Trim(row["gateway_uuids"], ", ") == "" {
		return badRequest("route: choose a gateway, or BLOCK")
	}
	if _, ok := ruleFromUser[row["sip_from_user"]]; !ok {
		return badRequest("SIP from user: unknown choice")
	}
	if _, ok := ruleFromDisplay[row["sip_from_display"]]; !ok {
		return badRequest("SIP from display name: unknown choice")
	}
	if !ruleFromHost[row["sip_from_host"]] {
		return badRequest("SIP from host: unknown choice")
	}
	if !ruleCIDType[row["cid_type"]] {
		return badRequest("CID type: PAI, RPID or none")
	}
	if n := atoi(row["route_strip"], 0); n < 0 || n > 20 {
		return badRequest("strip digits: 0 to 20")
	}
	return nil
}

func parseRange(s string) (lo, hi int, ok bool) {
	m := reRuleRange.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	lo, _ = strconv.Atoi(m[1])
	hi = lo
	if m[2] != "" {
		hi, _ = strconv.Atoi(m[2])
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	return lo, hi, true
}

// rulePattern is the number pattern of a prefix and a length (counted in
// digits, the prefix included; a leading + does not count).
func rulePattern(prefix, length string) (string, error) {
	plus := strings.HasPrefix(prefix, "+")
	body := strings.TrimPrefix(prefix, "+")
	lead := `^\+?`
	if plus {
		lead = `^\+`
	}
	if length == "" {
		return lead + "(" + regexp.QuoteMeta(body) + `\d*)$`, nil
	}
	var alts []string
	for _, part := range strings.Split(length, ",") {
		lo, hi, _ := parseRange(part)
		if lo < len(body) {
			lo = len(body)
		}
		if hi < lo {
			return "", badRequest("length %s is shorter than the prefix", part)
		}
		rest := fmt.Sprintf(`\d{%d}`, lo-len(body))
		if hi > lo {
			rest = fmt.Sprintf(`\d{%d,%d}`, lo-len(body), hi-len(body))
		}
		alts = append(alts, regexp.QuoteMeta(body)+rest)
	}
	return lead + "(" + strings.Join(alts, "|") + ")$", nil
}

// rangeRegex matches the decimal numbers lo..hi (no leading zeros).
func rangeRegex(lo, hi int) string {
	var parts []string
	start := lo
	for _, stop := range rangeStops(lo, hi) {
		parts = append(parts, rangePattern(start, stop))
		start = stop + 1
	}
	return strings.Join(parts, "|")
}

func rangeStops(lo, hi int) []int {
	set := map[int]bool{hi: true}
	for k := 1; ; k++ {
		stop := fillNines(lo, k)
		if !(lo <= stop && stop < hi) {
			break
		}
		set[stop] = true
	}
	for k := 1; ; k++ {
		stop := fillZeros(hi+1, k) - 1
		if !(lo < stop && stop <= hi) {
			break
		}
		set[stop] = true
	}
	var out []int
	for s := range set {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

func fillNines(n, k int) int {
	s := strconv.Itoa(n)
	v, _ := strconv.Atoi(s[:max(0, len(s)-k)] + strings.Repeat("9", k))
	return v
}

func fillZeros(n, k int) int { return n - n%pow10(k) }

func pow10(k int) int {
	p := 1
	for range k {
		p *= 10
	}
	return p
}

func rangePattern(start, stop int) string {
	a, b := strconv.Itoa(start), strconv.Itoa(stop)
	var out strings.Builder
	free := 0 // trailing positions that take any digit
	for i := range a {
		if a[i] == b[i] {
			out.WriteByte(a[i])
		} else if a[i] == '0' && b[i] == '9' {
			free++
		} else {
			fmt.Fprintf(&out, "[%c-%c]", a[i], b[i])
		}
	}
	if free == 1 {
		out.WriteString(`\d`)
	} else if free > 1 {
		fmt.Fprintf(&out, `\d{%d}`, free)
	}
	return out.String()
}

// callerFilter is the regular expression of the extensions a rule is for
// ("" = everybody).
func callerFilter(row map[string]string) string {
	var alts []string
	for _, e := range strings.Split(row["route_from_extensions"], ",") {
		if e = strings.TrimSpace(e); e != "" {
			alts = append(alts, regexp.QuoteMeta(e))
		}
	}
	for _, r := range strings.Split(row["route_from_ranges"], ",") {
		if lo, hi, ok := parseRange(r); ok {
			alts = append(alts, rangeRegex(lo, hi))
		}
	}
	if len(alts) == 0 {
		return ""
	}
	return "^(?:" + strings.Join(alts, "|") + ")$"
}
