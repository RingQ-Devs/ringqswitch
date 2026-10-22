package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
)

type apiCommand struct {
	usage string
	help  string
	fn    func(c *Core, args []string) (string, error)
}

var apiCommands map[string]apiCommand

func init() {
	apiCommands = map[string]apiCommand{
		"help":           {"help", "list commands", apiHelp},
		"status":         {"status", "switch status", apiStatus},
		"version":        {"version", "version string", func(*Core, []string) (string, error) { return "RingQswitch " + Version + "\n", nil }},
		"show":           {"show channels|calls|registrations|gateways|applications [as json]", "list objects", apiShow},
		"uuid_kill":      {"uuid_kill <uuid> [cause]", "hang up a channel", apiUUIDKill},
		"uuid_transfer":  {"uuid_transfer <uuid> <destination> [context]", "transfer a channel", apiUUIDTransfer},
		"uuid_dump":      {"uuid_dump <uuid>", "show channel variables", apiUUIDDump},
		"uuid_send_dtmf": {"uuid_send_dtmf <uuid> <digits>", "send DTMF (SIP INFO) to a channel", apiUUIDSendDTMF},
		"originate":      {"originate <dialstring> <extension>|&<app>(<data>) [XML] [context] [cid_name] [cid_num] [timeout_sec]", "place a call", apiOriginate},
		"hupall":         {"hupall [cause]", "hang up every channel", apiHupall},
		"reloadxml":      {"reloadxml", "reload directory, dialplan and ACLs", apiReload},
		"blocked":        {"blocked", "list addresses blocked for failed authentication", apiBlocked},
		"unblock":        {"unblock <ip>", "remove an address from the block list", apiUnblock},
	}
}

// API executes a command line and returns its output. Errors are returned
// as "-ERR ..." text, as clients expect.
func (c *Core) API(line string) string {
	f := splitArgs(line)
	if len(f) == 0 {
		return "-ERR no command\n"
	}
	cmd, ok := apiCommands[strings.ToLower(f[0])]
	if !ok {
		return "-ERR " + f[0] + " Command not found!\n"
	}
	out, err := cmd.fn(c, f[1:])
	if err != nil {
		return "-ERR " + err.Error() + "\n"
	}
	return out
}

// splitArgs splits a command line on spaces, keeping {...} and [...] blocks
// and quoted strings together. Quotes around a whole argument are removed;
// quotes inside a block (e.g. {var='a b'}) are kept for the dial string parser.
func splitArgs(line string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	var quote byte
	whole := false // current argument started with a quote
	flush := func() {
		if cur.Len() == 0 && !whole {
			return
		}
		arg := cur.String()
		out = append(out, arg)
		cur.Reset()
		whole = false
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				if depth > 0 {
					cur.WriteByte(c)
				}
				continue
			}
			cur.WriteByte(c)
		case (c == '\'' || c == '"') && depth == 0 && cur.Len() == 0:
			quote, whole = c, true
		case (c == '\'' || c == '"') && depth > 0:
			quote = c
			cur.WriteByte(c)
		case c == '{' || c == '[':
			depth++
			cur.WriteByte(c)
		case c == '}' || c == ']':
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
		case (c == ' ' || c == '\t') && depth == 0:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func apiHelp(*Core, []string) (string, error) {
	names := make([]string, 0, len(apiCommands))
	for n := range apiCommands {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	for _, n := range names {
		fmt.Fprintf(tw, "%s\t%s\n", apiCommands[n].usage, apiCommands[n].help)
	}
	tw.Flush()
	return b.String(), nil
}

func apiStatus(c *Core, _ []string) (string, error) {
	up := time.Since(c.started).Round(time.Second)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := c.config().Settings
	srv, cli := c.stack.Counts()
	st := c.stack.Stats()
	return fmt.Sprintf("UP %s\nRingQswitch %s is ready (core %s)\n%d session(s) since startup\n%d session(s) active - max %d\n%d registration(s)\n%d address(es) blocked for failed authentication\n%d rtp port pair(s) in use (range %d-%d)\n%d sip transaction(s) (%d server, %d client)\nsip messages: %d received, %d sent, %d not SIP, %d invalid, %d from blocked addresses, %d send errors\nSIP %s:%d (tcp=%v) domain %s\nmemory %d MiB, %d goroutines\n",
		up, Version, c.coreUUID, c.sessionsTotal.Load(), c.channelCount(), s.MaxSessions, c.reg.count(), len(c.guard.addresses()),
		c.media.InUse(), s.RTPStartPort, s.RTPEndPort, srv+cli, srv, cli, st.Received, st.Sent, st.Unparsable, st.Invalid, st.Filtered, st.SendErrors, s.SIPIP, c.stack.Port(), s.EnableTCP, s.Domain,
		ms.Alloc>>20, runtime.NumGoroutine()), nil
}

// ChannelInfo is a snapshot of a channel for listings.
// Field names are the stable "show channels as json" row fields.
type ChannelInfo struct {
	UUID         string `json:"uuid"`
	Direction    string `json:"direction"`
	Created      string `json:"created"`
	CreatedEpoch string `json:"created_epoch"`
	Name         string `json:"name"`
	State        string `json:"state"`
	CallState    string `json:"callstate"`
	CIDName      string `json:"cid_name"`
	CIDNum       string `json:"cid_num"`
	IPAddr       string `json:"ip_addr"`
	Destination  string `json:"dest"`
	Application  string `json:"application"`
	Context      string `json:"context"`
	Codec        string `json:"read_codec"`
	CallUUID     string `json:"call_uuid"`
	Partner      string `json:"bridged_to"`
	Domain       string `json:"domain_name,omitempty"`
	AnsweredAt   int64  `json:"answered_epoch,omitempty"`
	CalleeNum    string `json:"callee_num,omitempty"`
	CalleeName   string `json:"callee_name,omitempty"`
	Trunk        string `json:"trunk,omitempty"` // the gateway (or carrier ACL) of a leg to or from a provider
}

// Snapshot returns listing information.
func (ch *Channel) Snapshot() ChannelInfo {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	info := ChannelInfo{
		UUID: ch.UUID, Direction: ch.Direction, Created: ch.Created.Format("2006-01-02 15:04:05"),
		CreatedEpoch: strconv.FormatInt(ch.Created.Unix(), 10),
		Name:         ch.vars["channel_name"], State: ch.state, CallState: ch.callState,
		CIDName: ch.vars["caller_id_name"], CIDNum: ch.vars["caller_id_number"], IPAddr: ch.vars["network_addr"],
		Destination: ch.vars["destination_number"], Application: ch.app, Context: ch.vars["context"],
		CallUUID:  firstNonEmpty(ch.vars["originator"], ch.UUID),
		Domain:    firstNonEmpty(ch.vars["domain_name"], ch.vars["dialed_domain"]),
		CalleeNum: firstNonEmpty(ch.peerCIDNum, ch.vars["callee_id_number"]), CalleeName: firstNonEmpty(ch.peerCIDName, ch.vars["callee_id_name"]),
		Trunk: firstNonEmpty(ch.vars["gateway_name"], ch.vars["acl_name"]),
	}
	if ch.answered && !ch.answeredAt.IsZero() {
		info.AnsweredAt = ch.answeredAt.Unix()
	}
	if ch.partner != nil {
		info.Partner = ch.partner.UUID
	}
	if ch.ep != nil {
		if sc, ok := ch.ep.SendCodec(); ok {
			info.Codec = sc.Name
		}
	}
	return info
}

func asJSON(args []string) bool {
	return len(args) >= 2 && strings.EqualFold(args[len(args)-2], "as") && strings.EqualFold(args[len(args)-1], "json")
}

// rowsJSON renders the {"row_count": N, "rows": [...]} shape.
func rowsJSON[T any](rows []T) (string, error) {
	return toJSON(struct {
		RowCount int `json:"row_count"`
		Rows     []T `json:"rows"`
	}{len(rows), rows})
}

func toJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return string(b) + "\n", err
}

func apiShow(c *Core, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: %s", apiCommands["show"].usage)
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	if len(args) >= 2 && strings.EqualFold(args[1], "count") {
		switch strings.ToLower(args[0]) {
		case "channels":
			return fmt.Sprintf("\n%d total.\n", c.channelCount()), nil
		case "calls":
			n := 0
			for _, ch := range c.Channels() {
				if info := ch.Snapshot(); !(info.Direction == DirectionOutbound && info.Partner != "") {
					n++
				}
			}
			return fmt.Sprintf("\n%d total.\n", n), nil
		case "registrations":
			return fmt.Sprintf("\n%d total.\n", c.reg.count()), nil
		}
	}
	switch strings.ToLower(args[0]) {
	case "channels", "calls":
		infos := []ChannelInfo{}
		for _, ch := range c.Channels() {
			info := ch.Snapshot()
			if args[0] == "calls" && info.Direction == DirectionOutbound && info.Partner != "" {
				continue // listed with its A leg
			}
			infos = append(infos, info)
		}
		if asJSON(args) {
			return rowsJSON(infos)
		}
		fmt.Fprintln(tw, "uuid\tdirection\tcreated\tname\tstate\tcallstate\tcid_num\tdest\tapp\tcodec\tbridged_to")
		for _, i := range infos {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", i.UUID, i.Direction, i.Created, i.Name,
				i.State, i.CallState, i.CIDNum, i.Destination, i.Application, i.Codec, i.Partner)
		}
		tw.Flush()
		fmt.Fprintf(&b, "\n%d total.\n", len(infos))
	case "registrations":
		// JSON rows use the columns of the registrations table.
		type reg struct {
			User      string `json:"reg_user"`
			Realm     string `json:"realm"`
			Token     string `json:"token"`
			Contact   string `json:"url"`
			Expires   string `json:"expires"`
			IP        string `json:"network_ip"`
			Port      string `json:"network_port"`
			Proto     string `json:"network_proto"`
			Hostname  string `json:"hostname"`
			UserAgent string `json:"metadata"`
			ExpiresIn int    `json:"-"`
			Network   string `json:"-"`
		}
		host := c.events.hostname
		regs := []reg{}
		for _, r := range c.reg.All() {
			regs = append(regs, reg{
				User: r.User, Realm: r.Domain, Token: r.CallID, Contact: r.Contact,
				Expires: strconv.FormatInt(r.Expires.Unix(), 10), IP: r.Src.AP.Addr().String(),
				Port: strconv.Itoa(int(r.Src.AP.Port())), Proto: r.Src.Transport, Hostname: host,
				UserAgent: r.UserAgent, ExpiresIn: int(time.Until(r.Expires).Seconds()), Network: r.Src.String(),
			})
		}
		if asJSON(args) {
			return rowsJSON(regs)
		}
		fmt.Fprintln(tw, "user\tcontact\tnetwork\texpires\tuser_agent")
		for _, r := range regs {
			fmt.Fprintf(tw, "%s@%s\t%s\t%s\t%ds\t%s\n", r.User, r.Realm, r.Contact, r.Network, r.ExpiresIn, r.UserAgent)
		}
		tw.Flush()
		fmt.Fprintf(&b, "\n%d total.\n", len(regs))
	case "gateways":
		gws := c.gatewayList()
		sort.Slice(gws, func(i, j int) bool { return gws[i].cfg.Name < gws[j].cfg.Name })
		type gw struct {
			Name     string `json:"name"`
			Label    string `json:"label"`
			Proxy    string `json:"proxy"`
			Register bool   `json:"register"`
			State    string `json:"state"`
			Error    string `json:"last_error"`
		}
		list := []gw{}
		for _, g := range gws {
			g.mu.Lock()
			list = append(list, gw{g.cfg.Name, firstNonEmpty(g.cfg.Label, g.cfg.Name), g.cfg.Proxy, g.cfg.Register, g.state, g.lastErr})
			g.mu.Unlock()
		}
		if asJSON(args) {
			return rowsJSON(list)
		}
		fmt.Fprintln(tw, "name\tlabel\tproxy\tregister\tstate\tlast_error")
		for _, g := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s\n", g.Name, g.Label, g.Proxy, g.Register, g.State, g.Error)
		}
		tw.Flush()
		fmt.Fprintf(&b, "\n%d total.\n", len(list))
	case "applications", "apps":
		for _, n := range c.AppNames() {
			fmt.Fprintln(&b, n)
		}
	default:
		return "", fmt.Errorf("unknown object %q", args[0])
	}
	return b.String(), nil
}

func (c *Core) mustChannel(args []string, n int, usage string) (*Channel, error) {
	if len(args) < n {
		return nil, fmt.Errorf("usage: %s", usage)
	}
	ch := c.Channel(args[0])
	if ch == nil {
		return nil, fmt.Errorf("no such channel %s", args[0])
	}
	return ch, nil
}

func apiUUIDKill(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 1, apiCommands["uuid_kill"].usage)
	if err != nil {
		return "", err
	}
	cause := CauseManagerRequest
	if len(args) > 1 {
		cause = ParseCause(args[1])
	}
	ch.Hangup(cause)
	return "+OK\n", nil
}

func apiUUIDTransfer(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_transfer"].usage)
	if err != nil {
		return "", err
	}
	context := ""
	if len(args) > 2 {
		context = args[len(args)-1]
	}
	c.transfer(ch, args[1], context)
	return "+OK\n", nil
}

func apiUUIDDump(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 1, apiCommands["uuid_dump"].usage)
	if err != nil {
		return "", err
	}
	vars := ch.Vars()
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	info := ch.Snapshot()
	fmt.Fprintf(&b, "Channel-State: %s\nChannel-Call-State: %s\nChannel-Name: %s\n", info.State, info.CallState, info.Name)
	for _, k := range keys {
		fmt.Fprintf(&b, "variable_%s: %s\n", k, vars[k])
	}
	return b.String(), nil
}

func apiUUIDSendDTMF(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_send_dtmf"].usage)
	if err != nil {
		return "", err
	}
	for _, d := range args[1] {
		ch.SendDTMF(d, 250)
	}
	return "+OK\n", nil
}

func apiOriginate(c *Core, args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: %s", apiCommands["originate"].usage)
	}
	dial, dest := args[0], args[1]
	rest := args[2:]
	if len(rest) > 0 && strings.EqualFold(rest[0], "XML") {
		rest = rest[1:]
	}
	arg := func(i int) string {
		if i < len(rest) {
			return rest[i]
		}
		return ""
	}
	context := firstNonEmpty(arg(0), "default")
	vars := map[string]string{}
	if v := arg(1); v != "" {
		vars["origination_caller_id_name"] = v
	}
	if v := arg(2); v != "" {
		vars["origination_caller_id_number"] = v
	}
	timeout := 60
	if n, err := strconv.Atoi(arg(3)); err == nil && n > 0 {
		timeout = n
	}
	vars["call_timeout"] = strconv.Itoa(timeout)
	ctx, cancel := context2(time.Duration(timeout+5) * time.Second)
	defer cancel()
	b, cause := c.originate(ctx, nil, dial, vars)
	if b == nil {
		return "", fmt.Errorf("%s", cause.String())
	}
	if app, ok := strings.CutPrefix(dest, "&"); ok {
		name, data, _ := strings.Cut(app, "(")
		data = strings.TrimSuffix(data, ")")
		go c.runExecutorWith(b, []dialplan.Action{{App: name, Data: data}})
	} else {
		b.SetVar("destination_number", dest)
		b.SetVar("context", context)
		go c.runExecutor(b)
	}
	return "+OK " + b.UUID + "\n", nil
}

func context2(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func apiHupall(c *Core, args []string) (string, error) {
	cause := CauseManagerRequest
	if len(args) > 0 {
		cause = ParseCause(args[0])
	}
	n := 0
	for _, ch := range c.Channels() {
		ch.Hangup(cause)
		n++
	}
	return fmt.Sprintf("+OK hung up %d channel(s)\n", n), nil
}

func apiReload(c *Core, _ []string) (string, error) {
	if err := c.Reload(); err != nil {
		return "", err
	}
	return "+OK [Success]\n", nil
}

func apiBlocked(c *Core, _ []string) (string, error) {
	list := c.guard.list()
	var b strings.Builder
	ips := map[netip.Addr]bool{}
	for _, ban := range list {
		fmt.Fprintf(&b, "%s until %s (%s)\n", ban.IP, ban.Until.Format(time.RFC3339), ban.Jail)
		ips[ban.IP] = true
	}
	fmt.Fprintf(&b, "%d blocked.\n", len(ips))
	return b.String(), nil
}

func apiUnblock(c *Core, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: %s", apiCommands["unblock"].usage)
	}
	ip, err := netip.ParseAddr(args[0])
	if err != nil {
		return "", err
	}
	if !c.guard.unban(ip, "", "rq_cli") {
		return "", fmt.Errorf("%s is not blocked", ip)
	}
	return "+OK\n", nil
}
