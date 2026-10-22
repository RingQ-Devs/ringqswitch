package core

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/sip"
)

// Variable expansion:
//
//	${name}              channel variable, falling back to a global variable
//	$${name}             global variable
//	${name:offset:len}   substring of a variable
//	${api args}          output of an API function (a space marks a call)
//	${api(args)}         same, function style
//
// References nest: ${user_data ${destination_number}@${domain_name} var x}
// expands the inner references first.

const maxExpandDepth = 8

// varLookup reads a variable for expansion.
type varLookup func(name string) string

// expand substitutes variable references and API calls in s.
func (c *Core) expand(s string, get varLookup) string {
	return c.expandDepth(s, get, 0)
}

func (c *Core) expandDepth(s string, get varLookup, depth int) string {
	if !strings.Contains(s, "${") || depth > maxExpandDepth {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		global := i > 0 && s[i-1] == '$'
		end := matchBrace(s, i+1)
		if end < 0 {
			b.WriteString(s)
			return b.String()
		}
		if global {
			b.WriteString(s[:i-1])
		} else {
			b.WriteString(s[:i])
		}
		inner := c.expandDepth(s[i+2:end], get, depth+1)
		if global {
			b.WriteString(c.globalVar(inner))
		} else {
			b.WriteString(c.resolveRef(inner, get, depth))
		}
		s = s[end+1:]
	}
}

// matchBrace returns the index of the '}' closing the '{' at open.
func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var substrRef = regexp.MustCompile(`^([^:\s(]+):(-?\d+)(?::(\d+))?$`)

func (c *Core) resolveRef(ref string, get varLookup, depth int) string {
	if p := strings.IndexAny(ref, " ("); p > 0 {
		name, args := ref[:p], ref[p+1:]
		if ref[p] == '(' {
			args = strings.TrimSuffix(args, ")")
		}
		return c.callFunc(strings.ToLower(name), strings.TrimSpace(args), get, depth)
	}
	if m := substrRef.FindStringSubmatch(ref); m != nil {
		v := c.lookupVar(m[1], get)
		off, _ := strconv.Atoi(m[2])
		if off < 0 {
			off += len(v)
		}
		off = max(0, min(off, len(v)))
		v = v[off:]
		if m[3] != "" {
			if n, _ := strconv.Atoi(m[3]); n < len(v) {
				v = v[:n]
			}
		}
		return v
	}
	if v := c.lookupVar(ref, get); v != "" || ref != "create_uuid" {
		return v
	}
	return sip.NewUUID()
}

func (c *Core) lookupVar(name string, get varLookup) string {
	if get != nil {
		if v := get(name); v != "" {
			return v
		}
	}
	return c.globalVar(name)
}

func (c *Core) globalVar(name string) string {
	if v, ok := c.globals.get(name); ok {
		return v
	}
	return c.config().Vars[name]
}

// ---------------------------------------------------------------------------
// Expansion functions (API commands usable inside ${...})

func (c *Core) callFunc(name, args string, get varLookup, depth int) string {
	switch name {
	case "user_exists":
		// user_exists id <user> <domain>
		f := strings.Fields(args)
		if len(f) >= 2 {
			domain := ""
			if len(f) >= 3 {
				domain = f[2]
			}
			if c.config().Directory.LookupIn(domain, f[1]) != nil {
				return "true"
			}
		}
		return "false"
	case "user_data":
		return c.userData(args)
	case "domain_exists":
		if c.config().Directory.Find(args) != nil {
			return "true"
		}
		return "false"
	case "cond":
		return evalCond(args)
	case "strftime":
		return strftimeArg(args, time.Now())
	case "strepoch":
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", args, time.Local); err == nil {
			return strconv.FormatInt(t.Unix(), 10)
		}
		return strconv.FormatInt(time.Now().Unix(), 10)
	case "epoch":
		return strconv.FormatInt(time.Now().Unix(), 10)
	case "create_uuid", "uuid":
		return sip.NewUUID()
	case "regex":
		return regexFunc(args)
	case "sofia_contact":
		return c.sipContact(args)
	case "expr":
		if v, err := evalExpr(args); err == nil {
			return strconv.FormatInt(v, 10)
		}
		return "!err!"
	case "eval":
		return args
	case "global_getvar":
		return c.globalVar(args)
	case "uuid_getvar":
		f := strings.Fields(args)
		if len(f) == 2 {
			if ch := c.Channel(f[0]); ch != nil {
				return ch.Var(f[1])
			}
		}
		return "_undef_"
	case "uuid_exists":
		if c.Channel(args) != nil {
			return "true"
		}
		return "false"
	case "hash", "db":
		// hash select/<realm>/<key>
		op, rest, _ := strings.Cut(args, "/")
		if strings.EqualFold(op, "select") {
			realm, key, _ := strings.Cut(rest, "/")
			v, _ := c.hashes.get(name, realm, key)
			return v
		}
		if strings.EqualFold(op, "exists") {
			realm, key, _ := strings.Cut(rest, "/")
			if _, ok := c.hashes.get(name, realm, key); ok {
				return "true"
			}
			return "false"
		}
		return ""
	case "url_encode":
		return url.QueryEscape(args)
	case "url_decode":
		if v, err := url.QueryUnescape(args); err == nil {
			return v
		}
		return args
	case "md5":
		sum := md5.Sum([]byte(args))
		return hex.EncodeToString(sum[:])
	case "toupper":
		return strings.ToUpper(args)
	case "tolower":
		return strings.ToLower(args)
	case "sofia_count_reg", "count_reg":
		user, domain, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(args, "internal/"), "user/"), "@")
		return strconv.Itoa(len(c.reg.Lookup(user, c.domainOrDefault(domain))))
	case "registered", "user_registered":
		user, domain, _ := strings.Cut(args, "@")
		if len(c.reg.Lookup(user, c.domainOrDefault(domain))) > 0 {
			return "true"
		}
		return "false"
	}
	c.log.Debug("unknown expansion function", "name", name, "args", args)
	return ""
}

// domainOrDefault maps a domain name (or "") to the configured domain name.
func (c *Core) domainOrDefault(name string) string {
	dir := c.config().Directory
	if dom := dir.Resolve(name); dom != nil {
		return dom.Name
	}
	return name
}

// userData implements user_data <user>@<domain> <attr|param|var> <name>.
func (c *Core) userData(args string) string {
	f := strings.Fields(args)
	if len(f) < 3 {
		return ""
	}
	id, domain, _ := strings.Cut(f[0], "@")
	u := c.config().Directory.LookupIn(domain, id)
	if u == nil {
		return ""
	}
	key := f[2]
	switch strings.ToLower(f[1]) {
	case "attr":
		switch key {
		case "id":
			return u.ID
		case "number-alias":
			return u.Params["number-alias"]
		}
		return ""
	case "param":
		if key == "password" {
			return u.Password
		}
		return u.Params[strings.ToLower(key)]
	default: // var
		return u.Vars[key]
	}
}

// sipContact returns a dial string for the most recent registration of
// user@domain, or error/user_not_registered.
func (c *Core) sipContact(args string) string {
	spec := strings.TrimSpace(args)
	if i := strings.LastIndexByte(spec, '/'); i >= 0 {
		spec = spec[i+1:] // drop profile prefix ("*/", "internal/")
	}
	user, domain, _ := strings.Cut(spec, "@")
	if b := c.reg.Lookup(user, c.domainOrDefault(domain)); len(b) > 0 {
		return "ringq/internal/" + b[0].Contact
	}
	return "error/user_not_registered"
}

// ---------------------------------------------------------------------------
// cond, regex, expr, strftime

// evalCond implements "cond <a> <op> <b> ? <true> : <false>".
func evalCond(s string) string {
	q := strings.Index(s, "?")
	if q < 0 {
		return ""
	}
	expr, rest := strings.TrimSpace(s[:q]), s[q+1:]
	colon := strings.LastIndex(rest, ":")
	yes, no := rest, ""
	if colon >= 0 {
		yes, no = rest[:colon], rest[colon+1:]
	}
	yes, no = unquoteCond(strings.TrimSpace(yes)), unquoteCond(strings.TrimSpace(no))
	if condTrue(expr) {
		return yes
	}
	return no
}

func unquoteCond(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

func condTrue(expr string) bool {
	for _, op := range []string{"==", "!=", ">=", "<=", ">", "<"} {
		i := strings.Index(expr, op)
		if i < 0 {
			continue
		}
		a := unquoteCond(strings.TrimSpace(expr[:i]))
		b := unquoteCond(strings.TrimSpace(expr[i+len(op):]))
		an, aerr := strconv.ParseFloat(a, 64)
		bn, berr := strconv.ParseFloat(b, 64)
		numeric := aerr == nil && berr == nil
		cmp := strings.Compare(a, b)
		if numeric {
			switch {
			case an < bn:
				cmp = -1
			case an > bn:
				cmp = 1
			default:
				cmp = 0
			}
		}
		switch op {
		case "==":
			return cmp == 0
		case "!=":
			return cmp != 0
		case ">=":
			return cmp >= 0
		case "<=":
			return cmp <= 0
		case ">":
			return cmp > 0
		case "<":
			return cmp < 0
		}
	}
	expr = strings.TrimSpace(unquoteCond(expr))
	return truthy(expr) || (expr != "" && expr != "0" && !strings.EqualFold(expr, "false"))
}

var regexCache sync.Map

func cachedRegexp(p string) (*regexp.Regexp, error) {
	if re, ok := regexCache.Load(p); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, err
	}
	regexCache.Store(p, re)
	return re, nil
}

// regexFunc implements "regex <data>|<pattern>[|<substitution>]"; %1..%9
// in the substitution refer to captures. Without one it returns true/false.
func regexFunc(args string) string {
	parts := strings.SplitN(args, "|", 3)
	if len(parts) < 2 {
		return "false"
	}
	re, err := cachedRegexp(parts[1])
	if err != nil {
		return "false"
	}
	m := re.FindStringSubmatch(parts[0])
	if len(parts) == 2 {
		if m != nil {
			return "true"
		}
		return "false"
	}
	if m == nil {
		return ""
	}
	out := parts[2]
	for i := len(m) - 1; i >= 0; i-- {
		out = strings.ReplaceAll(out, "%"+strconv.Itoa(i), m[i])
		out = strings.ReplaceAll(out, "$"+strconv.Itoa(i), m[i])
	}
	return out
}

// evalExpr evaluates integer arithmetic: + - * / % and parentheses.
func evalExpr(s string) (int64, error) {
	p := &exprParser{s: strings.ReplaceAll(s, " ", "")}
	v, err := p.sum()
	if err == nil && p.i != len(p.s) {
		err = fmt.Errorf("unexpected %q", p.s[p.i:])
	}
	return v, err
}

type exprParser struct {
	s string
	i int
}

func (p *exprParser) sum() (int64, error) {
	v, err := p.product()
	for err == nil && p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
		op := p.s[p.i]
		p.i++
		var r int64
		if r, err = p.product(); err == nil {
			if op == '+' {
				v += r
			} else {
				v -= r
			}
		}
	}
	return v, err
}

func (p *exprParser) product() (int64, error) {
	v, err := p.unary()
	for err == nil && p.i < len(p.s) && strings.IndexByte("*/%", p.s[p.i]) >= 0 {
		op := p.s[p.i]
		p.i++
		var r int64
		if r, err = p.unary(); err != nil {
			break
		}
		switch {
		case op == '*':
			v *= r
		case r == 0:
			return 0, fmt.Errorf("division by zero")
		case op == '/':
			v /= r
		default:
			v %= r
		}
	}
	return v, err
}

func (p *exprParser) unary() (int64, error) {
	if p.i < len(p.s) && p.s[p.i] == '-' {
		p.i++
		v, err := p.unary()
		return -v, err
	}
	if p.i < len(p.s) && p.s[p.i] == '(' {
		p.i++
		v, err := p.sum()
		if err != nil {
			return 0, err
		}
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return 0, fmt.Errorf("missing )")
		}
		p.i++
		return v, nil
	}
	start := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if start == p.i {
		return 0, fmt.Errorf("number expected at %d", start)
	}
	return strconv.ParseInt(p.s[start:p.i], 10, 64)
}

// strftimeArg implements "strftime [<epoch>|]<format>".
func strftimeArg(args string, now time.Time) string {
	if e, f, ok := strings.Cut(args, "|"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(e), 10, 64); err == nil {
			now, args = time.Unix(n, 0), f
		}
	}
	if args == "" {
		args = "%Y-%m-%d %T"
	}
	return Strftime(args, now)
}

// Strftime formats t with C strftime conversions.
func Strftime(format string, t time.Time) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 == len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] {
		case 'Y':
			b.WriteString(strconv.Itoa(t.Year()))
		case 'y':
			fmt.Fprintf(&b, "%02d", t.Year()%100)
		case 'm':
			fmt.Fprintf(&b, "%02d", int(t.Month()))
		case 'd':
			fmt.Fprintf(&b, "%02d", t.Day())
		case 'e':
			fmt.Fprintf(&b, "%2d", t.Day())
		case 'H':
			fmt.Fprintf(&b, "%02d", t.Hour())
		case 'I':
			h := t.Hour() % 12
			if h == 0 {
				h = 12
			}
			fmt.Fprintf(&b, "%02d", h)
		case 'M':
			fmt.Fprintf(&b, "%02d", t.Minute())
		case 'S':
			fmt.Fprintf(&b, "%02d", t.Second())
		case 'p':
			b.WriteString(t.Format("PM"))
		case 'b', 'h':
			b.WriteString(t.Format("Jan"))
		case 'B':
			b.WriteString(t.Format("January"))
		case 'a':
			b.WriteString(t.Format("Mon"))
		case 'A':
			b.WriteString(t.Format("Monday"))
		case 'j':
			fmt.Fprintf(&b, "%03d", t.YearDay())
		case 'u':
			wd := int(t.Weekday())
			if wd == 0 {
				wd = 7
			}
			b.WriteString(strconv.Itoa(wd))
		case 'w':
			b.WriteString(strconv.Itoa(int(t.Weekday())))
		case 'Z':
			b.WriteString(t.Format("MST"))
		case 'z':
			b.WriteString(t.Format("-0700"))
		case 's':
			b.WriteString(strconv.FormatInt(t.Unix(), 10))
		case 'F':
			b.WriteString(t.Format("2006-01-02"))
		case 'T':
			b.WriteString(t.Format("15:04:05"))
		case 'R':
			b.WriteString(t.Format("15:04"))
		case 'D':
			b.WriteString(t.Format("01/02/06"))
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case '%':
			b.WriteByte('%')
		default:
			b.WriteByte('%')
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Global variables set at runtime and the hash/db key-value stores

type globalVars struct {
	mu   sync.RWMutex
	vars map[string]string
}

func (g *globalVars) get(name string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	v, ok := g.vars[name]
	return v, ok
}

func (g *globalVars) set(name, value string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.vars == nil {
		g.vars = map[string]string{}
	}
	g.vars[name] = value
}

// kvStore backs the hash and db applications (in memory).
type kvStore struct {
	mu sync.Mutex
	m  map[string]string
}

const maxKVEntries = 100000

func (k *kvStore) key(ns, realm, key string) string { return ns + "\x00" + realm + "\x00" + key }

func (k *kvStore) get(ns, realm, key string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.m[k.key(ns, realm, key)]
	return v, ok
}

func (k *kvStore) set(ns, realm, key, value string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]string{}
	}
	if len(k.m) >= maxKVEntries {
		k.m = map[string]string{} // crude bound: these are caches, not records
	}
	k.m[k.key(ns, realm, key)] = value
}

func (k *kvStore) del(ns, realm, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, k.key(ns, realm, key))
}
