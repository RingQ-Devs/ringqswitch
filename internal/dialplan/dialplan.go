// Package dialplan implements the XML dialplan: contexts made
// of extensions, extensions made of regex conditions with actions and
// anti-actions.
package dialplan

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Action is one application call.
type Action struct {
	App    string
	Data   string
	Inline bool
}

// Regex is one <regex field=".." expression=".."/> child of a condition
// with a regex="all|any|xor" attribute.
type Regex struct {
	Field      string
	Expression string
	re         *regexp.Regexp
}

// Condition tests a channel field against a regular expression and/or the
// current time. Nested conditions are evaluated, in order, after the
// condition's own actions when it matches.
type Condition struct {
	Field       string
	Expression  string
	Break       string // on-false (default), on-true, always, never
	RegexMode   string // "", all, any, xor
	Regexes     []Regex
	re          *regexp.Regexp
	times       []timeRule
	Actions     []Action
	AntiActions []Action
	Nested      []*Condition
}

// Extension is an ordered group of conditions.
type Extension struct {
	Name       string
	Continue   bool
	Conditions []*Condition
}

// Context is a named list of extensions.
type Context struct {
	Name       string
	Extensions []*Extension
}

// Dialplan holds all contexts.
type Dialplan struct {
	Contexts map[string]*Context
}

// Vars gives the dialplan access to channel variables.
type Vars interface {
	Var(name string) string
	SetVar(name, value string)
	Expand(s string) string
}

// FieldVars is implemented by channels that resolve condition fields
// themselves (caller profile aliases, global fallbacks).
type FieldVars interface {
	Field(name string) string
}

// InlineRunner is implemented by channels that can execute applications
// marked inline="true" during the hunt. It returns false for applications
// that cannot run inline.
type InlineRunner interface {
	RunInline(app, data string) bool
}

// Tracer is told, line by line, what a hunt does (the console's
// "Dialplan:" log lines). Tracing returns false when nobody reads them.
type Tracer interface {
	Tracing() bool
	Trace(line string)
}

// New returns an empty dialplan.
func New() *Dialplan { return &Dialplan{Contexts: map[string]*Context{}} }

// xmlCondition is decoded by hand to keep children of any kind and depth.
type xmlCondition struct {
	attrs       map[string]string
	actions     []Action
	antiActions []Action
	regexes     []Regex
	nested      []*xmlCondition
}

func attrMap(se xml.StartElement) map[string]string {
	m := make(map[string]string, len(se.Attr))
	for _, a := range se.Attr {
		m[a.Name.Local] = a.Value
	}
	return m
}

// UnmarshalXML implements xml.Unmarshaler.
func (xc *xmlCondition) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	xc.attrs = attrMap(start)
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.StartElement:
			a := attrMap(t)
			switch t.Name.Local {
			case "action", "anti-action":
				act := Action{App: a["application"], Data: a["data"], Inline: isTrue(a["inline"])}
				if t.Name.Local == "action" {
					xc.actions = append(xc.actions, act)
				} else {
					xc.antiActions = append(xc.antiActions, act)
				}
				if err := dec.Skip(); err != nil {
					return err
				}
			case "regex":
				xc.regexes = append(xc.regexes, Regex{Field: a["field"], Expression: a["expression"]})
				if err := dec.Skip(); err != nil {
					return err
				}
			case "condition":
				child := &xmlCondition{}
				if err := child.UnmarshalXML(dec, t); err != nil {
					return err
				}
				xc.nested = append(xc.nested, child)
			default:
				if err := dec.Skip(); err != nil {
					return err
				}
			}
		}
	}
}

type xmlExtension struct {
	Name       string          `xml:"name,attr"`
	Continue   string          `xml:"continue,attr"`
	Conditions []*xmlCondition `xml:"condition"`
}

type xmlContext struct {
	Name       string         `xml:"name,attr"`
	Extensions []xmlExtension `xml:"extension"`
}

// Load parses an XML document and appends every <context> found in it
// (at any depth) to the dialplan. Extensions for a context that already
// exists are appended in document order.
func (d *Dialplan) Load(r io.Reader, source string) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "context" {
			continue
		}
		var xc xmlContext
		if err := dec.DecodeElement(&xc, &se); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if xc.Name == "" {
			return fmt.Errorf("%s: context without name", source)
		}
		ctx := d.context(xc.Name)
		for _, xe := range xc.Extensions {
			ext, err := buildExtension(xe)
			if err != nil {
				return fmt.Errorf("%s: context %s: %w", source, xc.Name, err)
			}
			ctx.Extensions = append(ctx.Extensions, ext)
		}
	}
}

func (d *Dialplan) context(name string) *Context {
	ctx := d.Contexts[name]
	if ctx == nil {
		ctx = &Context{Name: name}
		d.Contexts[name] = ctx
	}
	return ctx
}

// LoadExtensions parses a fragment holding <extension> elements (as stored
// in the RingQ portal's v_dialplans.dialplan_xml) and appends them to the named
// context. The fragment is validated completely before anything is added.
func (d *Dialplan) LoadExtensions(context string, r io.Reader, source string) (int, error) {
	dec := xml.NewDecoder(r)
	var exts []*Extension
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("%s: %w", source, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "extension" {
			continue
		}
		var xe xmlExtension
		if err := dec.DecodeElement(&xe, &se); err != nil {
			return 0, fmt.Errorf("%s: %w", source, err)
		}
		ext, err := buildExtension(xe)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", source, err)
		}
		exts = append(exts, ext)
	}
	ctx := d.context(context)
	ctx.Extensions = append(ctx.Extensions, exts...)
	return len(exts), nil
}

func buildExtension(xe xmlExtension) (*Extension, error) {
	ext := &Extension{Name: xe.Name, Continue: isTrue(xe.Continue)}
	for _, xcnd := range xe.Conditions {
		c, err := buildCondition(xcnd)
		if err != nil {
			return nil, fmt.Errorf("extension %s: %w", xe.Name, err)
		}
		ext.Conditions = append(ext.Conditions, c)
	}
	return ext, nil
}

func buildCondition(xc *xmlCondition) (*Condition, error) {
	a := xc.attrs
	c := &Condition{Field: a["field"], Expression: a["expression"], Break: strings.ToLower(a["break"]),
		RegexMode: strings.ToLower(a["regex"])}
	switch c.Break {
	case "", "on-false", "on-true", "always", "never":
	default:
		return nil, fmt.Errorf("invalid break %q", a["break"])
	}
	switch c.RegexMode {
	case "", "all", "any", "xor":
	default:
		return nil, fmt.Errorf("invalid regex mode %q", a["regex"])
	}
	if c.Expression != "" {
		re, err := regexp.Compile(c.Expression)
		if err != nil {
			return nil, err
		}
		c.re = re
	}
	for _, rx := range xc.regexes {
		re, err := regexp.Compile(rx.Expression)
		if err != nil {
			return nil, err
		}
		rx.re = re
		c.Regexes = append(c.Regexes, rx)
	}
	for _, kind := range []string{"wday", "mday", "mon", "year", "hour", "minute", "minute-of-day", "time-of-day", "date-time", "yday", "week", "mweek"} {
		spec := a[kind]
		if spec == "" {
			continue
		}
		rule, err := parseTimeRule(kind, spec)
		if err != nil {
			return nil, err
		}
		c.times = append(c.times, rule)
	}
	c.Actions = xc.actions
	c.AntiActions = xc.antiActions
	for _, n := range xc.nested {
		child, err := buildCondition(n)
		if err != nil {
			return nil, err
		}
		c.Nested = append(c.Nested, child)
	}
	return c, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1", "enabled":
		return true
	}
	return false
}

func fieldValue(v Vars, field string) string {
	if strings.Contains(field, "${") {
		return v.Expand(field)
	}
	if fv, ok := v.(FieldVars); ok {
		return fv.Field(field)
	}
	return v.Var(field)
}

// eval evaluates the condition, returning the match result and captures.
func (c *Condition) eval(v Vars, now time.Time) (bool, []string) {
	for _, t := range c.times {
		if !t.match(now) {
			return false, nil
		}
	}
	if len(c.Regexes) > 0 {
		var caps []string
		matches := 0
		for _, rx := range c.Regexes {
			m := rx.re.FindStringSubmatch(fieldValue(v, rx.Field))
			if m != nil {
				matches++
				caps = m
				if c.RegexMode == "any" {
					break
				}
			} else if c.RegexMode == "all" || c.RegexMode == "" {
				return false, nil
			}
		}
		switch c.RegexMode {
		case "any":
			return matches > 0, caps
		case "xor":
			return matches == 1, caps
		}
		return true, caps
	}
	if c.re == nil {
		return true, nil
	}
	caps := c.re.FindStringSubmatch(fieldValue(v, c.Field))
	return caps != nil, caps
}

// Hunt returns the actions to execute for a channel in the given context.
// Inline actions run immediately so later conditions can see their effect.
func (d *Dialplan) Hunt(context string, v Vars, now time.Time) ([]Action, []string, error) {
	ctx := d.Contexts[context]
	if ctx == nil {
		return nil, nil, fmt.Errorf("dialplan: context %q not found", context)
	}
	var actions []Action
	var matched []string
	var tr func(string)
	if t, ok := v.(Tracer); ok && t.Tracing() {
		tr = t.Trace
	}
	for _, ext := range ctx.Extensions {
		if tr != nil {
			tr(fmt.Sprintf("parsing [%s->%s] continue=%t", context, ext.Name, ext.Continue))
		}
		proceed := false
		for _, c := range ext.Conditions {
			ok := c.walk(v, now, &actions, tr, ext.Name)
			proceed = ok
			if c.stops(ok) {
				break
			}
		}
		if proceed {
			matched = append(matched, ext.Name)
			if !ext.Continue {
				break
			}
		}
	}
	return actions, matched, nil
}

func (c *Condition) stops(ok bool) bool {
	brk := c.Break
	if brk == "" {
		brk = "on-false"
	}
	return (brk == "on-false" && !ok) || (brk == "on-true" && ok) || brk == "always"
}

// walk evaluates a condition, collects (or runs inline) its actions and then
// its nested conditions. tr (if not nil) gets the trace.
func (c *Condition) walk(v Vars, now time.Time, out *[]Action, tr func(string), ext string) bool {
	ok, caps := c.eval(v, now)
	if tr != nil {
		c.trace(v, now, ok, tr, ext)
	}
	list, kind := c.AntiActions, "ANTI-Action"
	if ok {
		list, kind = c.Actions, "Action"
	}
	for _, a := range list {
		a.Data = SubstituteCaptures(a.Data, caps)
		if tr != nil {
			inline := ""
			if a.Inline {
				inline = " INLINE"
			}
			tr(fmt.Sprintf("%s %s(%s)%s", kind, a.App, a.Data, inline))
		}
		if a.Inline {
			runInline(a, v)
			continue
		}
		*out = append(*out, a)
	}
	if ok {
		for _, n := range c.Nested {
			if n.stops(n.walk(v, now, out, tr, ext)) {
				break
			}
		}
	}
	return ok
}

// trace describes a condition's result in the console's trace format.
func (c *Condition) trace(v Vars, now time.Time, ok bool, tr func(string), ext string) {
	brk := c.Break
	if brk == "" {
		brk = "on-false"
	}
	res := map[bool]string{true: "PASS", false: "FAIL"}
	if len(c.times) > 0 {
		pass := true
		for _, t := range c.times {
			pass = pass && t.match(now)
		}
		tr(fmt.Sprintf("Date/TimeMatch (%s) [%s] break=%s", res[pass], ext, brk))
	}
	for _, rx := range c.Regexes {
		val := fieldValue(v, rx.Field)
		tr(fmt.Sprintf("Regex (%s) [%s] %s(%s) =~ /%s/ break=%s", res[rx.re.MatchString(val)], ext, rx.Field, val, rx.Expression, brk))
	}
	switch {
	case c.re != nil:
		val := fieldValue(v, c.Field)
		tr(fmt.Sprintf("Regex (%s) [%s] %s(%s) =~ /%s/ break=%s", res[ok], ext, c.Field, val, c.Expression, brk))
	case len(c.times) == 0 && len(c.Regexes) == 0:
		tr(fmt.Sprintf("Absolute Condition [%s]", ext))
	}
}

func runInline(a Action, v Vars) {
	if r, ok := v.(InlineRunner); ok && r.RunInline(a.App, a.Data) {
		return
	}
	switch a.App {
	case "set", "export":
		name, value, _ := strings.Cut(a.Data, "=")
		v.SetVar(strings.TrimPrefix(strings.TrimSpace(name), "nolocal:"), v.Expand(value))
	case "unset":
		v.SetVar(strings.TrimSpace(a.Data), "")
	}
}

// SubstituteCaptures replaces $0..$9 with regex captures.
func SubstituteCaptures(s string, caps []string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '$' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			n := int(s[i+1] - '0')
			if n < len(caps) {
				b.WriteString(caps[n])
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Time of day rules

type timeRule struct {
	kind   string
	ranges [][2]int
}

var wdayNames = map[string]int{"sun": 1, "mon": 2, "tue": 3, "wed": 4, "thu": 5, "fri": 6, "sat": 7}

const dateTimeLayout = "2006-01-02 15:04:05"

func parseDateTime(s string) (int, error) {
	s = strings.TrimSpace(s)
	if strings.Count(s, ":") == 1 {
		s += ":00"
	}
	t, err := time.ParseInLocation(dateTimeLayout, s, time.Local)
	if err != nil {
		return 0, fmt.Errorf("bad date-time %q", s)
	}
	return int(t.Unix()), nil
}

func parseTimeRule(kind, spec string) (timeRule, error) {
	r := timeRule{kind: kind}
	if kind == "date-time" {
		// "2024-12-25 00:00~2024-12-26 00:00" (the dialplan uses ~ as the
		// range separator here because dates contain dashes).
		for _, part := range strings.Split(spec, ",") {
			lo, hi, ok := strings.Cut(part, "~")
			if !ok {
				return r, fmt.Errorf("date-time %q: expected start~end", part)
			}
			a, err := parseDateTime(lo)
			if err != nil {
				return r, err
			}
			b, err := parseDateTime(hi)
			if err != nil {
				return r, err
			}
			r.ranges = append(r.ranges, [2]int{a, b})
		}
		return r, nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := parseTimeValue(kind, lo)
		if err != nil {
			return r, err
		}
		b := a
		if isRange {
			if b, err = parseTimeValue(kind, hi); err != nil {
				return r, err
			}
		}
		r.ranges = append(r.ranges, [2]int{a, b})
	}
	if len(r.ranges) == 0 {
		return r, fmt.Errorf("empty %s rule", kind)
	}
	return r, nil
}

func parseTimeValue(kind, s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch kind {
	case "wday":
		if len(s) >= 3 {
			if v, ok := wdayNames[s[:3]]; ok {
				return v, nil
			}
		}
	case "time-of-day":
		parts := strings.Split(s, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return 0, fmt.Errorf("bad time-of-day %q", s)
		}
		secs := 0
		for i, mult := range []int{3600, 60, 1} {
			if i >= len(parts) {
				break
			}
			n, err := strconv.Atoi(parts[i])
			if err != nil || n < 0 {
				return 0, fmt.Errorf("bad time-of-day %q", s)
			}
			secs += n * mult
		}
		return secs, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad %s value %q", kind, s)
	}
	return n, nil
}

func (r timeRule) match(now time.Time) bool {
	var v int
	switch r.kind {
	case "wday":
		v = int(now.Weekday()) + 1
	case "mday":
		v = now.Day()
	case "mon":
		v = int(now.Month())
	case "year":
		v = now.Year()
	case "hour":
		v = now.Hour()
	case "minute":
		v = now.Minute()
	case "minute-of-day":
		v = now.Hour()*60 + now.Minute() + 1
	case "time-of-day":
		v = now.Hour()*3600 + now.Minute()*60 + now.Second()
	case "yday":
		v = now.YearDay()
	case "week":
		v = (now.YearDay()-1)/7 + 1
	case "mweek":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		v = (now.Day()+int(first.Weekday())-1)/7 + 1
	case "date-time":
		// Ranges are written in wall-clock time: compare the time of day
		// in now's zone (the call's company) as if it were the server's.
		v = int(time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.Local).Unix())
		for _, rg := range r.ranges {
			if v >= rg[0] && v < rg[1] {
				return true
			}
		}
		return false
	}
	for _, rg := range r.ranges {
		lo, hi := rg[0], rg[1]
		if lo <= hi {
			if v >= lo && v <= hi {
				return true
			}
		} else if v >= lo || v <= hi { // wraps (e.g. 22:00-06:00, sat-mon)
			return true
		}
	}
	return false
}
