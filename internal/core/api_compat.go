package core

import (
	"context"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
)

// API commands RingQ backends use over the
// event socket.

func init() {
	for name, cmd := range map[string]apiCommand{
		"sip":               {"sip status [gateway <name>] | sip profile <name> rescan|restart|killgw <gw>|flush_inbound_reg [user@domain]|register <gw>|unregister <gw>", "SIP profile and gateway control", apiSIP},
		"sofia":             {"sofia ... (as sip ...)", "alias of sip, for existing portal integrations", apiSIP},
		"memcache":          {"memcache delete <key> | flush", "invalidate cached configuration (reloads it)", apiMemcache},
		"xml_flush_cache":   {"xml_flush_cache", "reload configuration", apiMemcache},
		"cache":             {"cache flush", "reload configuration", apiMemcache},
		"uuid_setvar":       {"uuid_setvar <uuid> <var> [value]", "set (or unset) a channel variable", apiUUIDSetVar},
		"uuid_setvar_multi": {"uuid_setvar_multi <uuid> <var>=<value>;<var>=<value>", "set channel variables", apiUUIDSetVarMulti},
		"uuid_getvar":       {"uuid_getvar <uuid> <var>", "read a channel variable", apiUUIDGetVar},
		"uuid_exists":       {"uuid_exists <uuid>", "true if the channel exists", apiUUIDExists},
		"uuid_answer":       {"uuid_answer <uuid>", "answer a channel", apiUUIDAnswer},
		"uuid_park":         {"uuid_park <uuid>", "park a channel", apiUUIDPark},
		"uuid_bridge":       {"uuid_bridge <uuid> <other_uuid>", "bridge two channels", apiUUIDBridge},
		"uuid_record":       {"uuid_record <uuid> start|stop <path>", "record a call", apiUUIDRecord},
		"uuid_broadcast":    {"uuid_broadcast <uuid> <path> [aleg|bleg|both]", "play a file to a channel", apiUUIDBroadcast},
		"uuid_hold":         {"uuid_hold [off|toggle] <uuid>", "hold (not supported yet)", apiNotSupported},
		"global_getvar":     {"global_getvar [name]", "read global variables", apiGlobalGetvar},
		"global_setvar":     {"global_setvar <name>=<value>", "set a global variable", apiGlobalSetvar},
		"fsctl":             {"fsctl send_sighup|shutdown ...", "core control (send_sighup reloads)", apiFsctl},
		"module_exists":     {"module_exists <module>", "true for modules RingQswitch replaces", apiModuleExists},
		"load":              {"load <module>", "accepted for compatibility", apiOK},
		"reload":            {"reload <module>", "reload configuration", apiReloadModule},
		"unload":            {"unload <module>", "accepted for compatibility", apiOK},
		"uptime":            {"uptime [s|m|h|d|ms]", "time since startup", apiUptime},
		"hostname":          {"hostname", "switch hostname", apiHostname},
		"switchname":        {"switchname", "switch name", apiHostname},
		"domains":           {"domains", "list domains and their user counts", apiDomains},
		"compat":            {"compat", "report dialplan applications and functions RingQswitch does not support", apiCompat},
	} {
		apiCommands[name] = cmd
	}
	// Functions usable in ${...} are also API commands (user_exists,
	// user_data, sofia_contact, create_uuid, strftime, expr, ...).
	for _, name := range []string{"user_exists", "user_data", "domain_exists", "cond", "strftime", "strepoch",
		"create_uuid", "regex", "sofia_contact", "expr", "eval", "url_encode", "url_decode", "md5",
		"sofia_count_reg", "hash", "db", "uuid_exists_func", "epoch"} {
		n := name
		if _, taken := apiCommands[n]; taken {
			continue
		}
		apiCommands[n] = apiCommand{n + " <args>", "expansion function", func(c *Core, args []string) (string, error) {
			return c.callFunc(n, strings.Join(args, " "), nil, 0), nil
		}}
	}
}

func apiOK(*Core, []string) (string, error) { return "+OK\n", nil }

func apiNotSupported(*Core, []string) (string, error) {
	return "", fmt.Errorf("not supported by RingQswitch yet")
}

func apiMemcache(c *Core, _ []string) (string, error) {
	c.requestReload()
	return "+OK\n", nil
}

// requestReload schedules a configuration reload shortly, so a burst of
// cache invalidations (one per edited row) causes a single reload.
func (c *Core) requestReload() {
	c.reloadReqMu.Lock()
	defer c.reloadReqMu.Unlock()
	if c.reloadPending {
		return
	}
	c.reloadPending = true
	time.AfterFunc(750*time.Millisecond, func() {
		c.reloadReqMu.Lock()
		c.reloadPending = false
		c.reloadReqMu.Unlock()
		if err := c.Reload(); err != nil {
			c.log.Error("reload failed", "err", err)
		}
	})
}

func apiReloadModule(c *Core, args []string) (string, error) {
	if len(args) > 0 && strings.HasPrefix(args[0], "mod_") {
		c.requestReload()
	}
	return "+OK Reloading XML\n+OK module reloaded\n", nil
}

func apiFsctl(c *Core, args []string) (string, error) {
	if len(args) > 0 && strings.EqualFold(args[0], "send_sighup") {
		if err := c.Reload(); err != nil {
			return "", err
		}
	}
	return "+OK\n", nil
}

func apiModuleExists(_ *Core, args []string) (string, error) {
	if len(args) == 0 {
		return "false", nil
	}
	switch args[0] {
	case "mod_sofia", "mod_dptools", "mod_dialplan_xml", "mod_event_socket", "mod_commands",
		"mod_loopback", "mod_hash", "mod_db", "mod_expr", "mod_sndfile", "mod_tone_stream", "mod_xml_cdr":
		return "true", nil
	}
	return "false", nil
}

func apiUptime(c *Core, args []string) (string, error) {
	d := time.Since(c.started)
	unit := "s"
	if len(args) > 0 {
		unit = args[0]
	}
	var v int64
	switch unit {
	case "ms":
		v = d.Milliseconds()
	case "m":
		v = int64(d.Minutes())
	case "h":
		v = int64(d.Hours())
	case "d":
		v = int64(d.Hours() / 24)
	default:
		v = int64(d.Seconds())
	}
	return strconv.FormatInt(v, 10), nil
}

func apiHostname(c *Core, _ []string) (string, error) { return c.events.hostname, nil }

func apiGlobalGetvar(c *Core, args []string) (string, error) {
	if len(args) > 0 {
		return c.globalVar(args[0]), nil
	}
	vars := map[string]string{}
	for k, v := range c.config().Vars {
		vars[k] = v
	}
	c.globals.mu.RLock()
	for k, v := range c.globals.vars {
		vars[k] = v
	}
	c.globals.mu.RUnlock()
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if strings.Contains(k, "password") {
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", k, vars[k])
	}
	return b.String(), nil
}

func apiGlobalSetvar(c *Core, args []string) (string, error) {
	name, value, ok := strings.Cut(strings.Join(args, " "), "=")
	if !ok {
		return "", fmt.Errorf("usage: global_setvar <name>=<value>")
	}
	c.globals.set(strings.TrimSpace(name), value)
	return "+OK\n", nil
}

func apiUUIDSetVar(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_setvar"].usage)
	if err != nil {
		return "", err
	}
	ch.SetVar(args[1], strings.Join(args[2:], " "))
	return "+OK\n", nil
}

func apiUUIDSetVarMulti(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_setvar_multi"].usage)
	if err != nil {
		return "", err
	}
	for _, kv := range strings.Split(strings.Join(args[1:], " "), ";") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			ch.SetVar(strings.TrimSpace(k), v)
		}
	}
	return "+OK\n", nil
}

func apiUUIDGetVar(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_getvar"].usage)
	if err != nil {
		return "", err
	}
	if v := ch.Var(args[1]); v != "" {
		return v, nil
	}
	return "_undef_", nil
}

func apiUUIDExists(c *Core, args []string) (string, error) {
	if len(args) > 0 && c.Channel(args[0]) != nil {
		return "true", nil
	}
	return "false", nil
}

func apiUUIDAnswer(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 1, apiCommands["uuid_answer"].usage)
	if err != nil {
		return "", err
	}
	if err := ch.AnswerLocal(false); err != nil {
		return "", err
	}
	return "+OK\n", nil
}

func apiUUIDPark(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 1, apiCommands["uuid_park"].usage)
	if err != nil {
		return "", err
	}
	c.runOnChannel(ch, []dialplan.Action{{App: "park"}})
	return "+OK\n", nil
}

// runOnChannel interrupts whatever a channel is doing and runs actions on
// it (detaching it from a bridge if needed).
func (c *Core) runOnChannel(ch *Channel, actions []dialplan.Action) {
	ch.mu.Lock()
	if ch.hungup {
		ch.mu.Unlock()
		return
	}
	if ch.hasExecutor {
		ch.pendingTransfer = &transferReq{actions: actions}
		if ch.appCancel != nil {
			ch.appCancel()
		}
		ch.mu.Unlock()
		return
	}
	ch.hasExecutor = true
	ctl := ch.bctl
	ch.mu.Unlock()
	if ctl != nil {
		ctl.Detach()
	}
	go c.runExecutorWith(ch, actions)
}

// adopt stops a channel's executor (without hanging up) and detaches it
// from its bridge so another channel's bridge can drive it.
func (c *Core) adopt(ch *Channel) {
	ch.mu.Lock()
	if ch.hasExecutor {
		ch.pendingTransfer = &transferReq{adopt: true}
		if ch.appCancel != nil {
			ch.appCancel()
		}
	}
	ctl := ch.bctl
	ch.mu.Unlock()
	if ctl != nil {
		ctl.Detach()
	}
}

// uuid_bridge <uuid> <other>: connect two existing channels.
func apiUUIDBridge(c *Core, args []string) (string, error) {
	a, err := c.mustChannel(args, 2, apiCommands["uuid_bridge"].usage)
	if err != nil {
		return "", err
	}
	b := c.Channel(args[1])
	if b == nil {
		return "", fmt.Errorf("no such channel %s", args[1])
	}
	if a == b {
		return "", fmt.Errorf("cannot bridge a channel to itself")
	}
	// Free b from what it is doing; a's bridge will drive it.
	c.adopt(b)
	c.runOnChannel(a, []dialplan.Action{{App: "uuid_bridge_to", Data: b.UUID}})
	return "+OK " + b.UUID + "\n", nil
}

// appBridgeTo bridges the channel to an existing one (used by uuid_bridge).
func appBridgeTo(ctx context.Context, c *Core, ch *Channel, data string) error {
	b := c.Channel(strings.TrimSpace(data))
	if b == nil {
		return fmt.Errorf("no such channel %s", data)
	}
	for _, x := range []*Channel{ch, b} {
		if x.Direction == DirectionInbound && !x.IsAnswered() {
			if err := x.AnswerLocal(false); err != nil {
				return err
			}
		}
	}
	// Wait (briefly) for b's executor to let go of it.
	for i := 0; i < 100; i++ {
		b.mu.Lock()
		busy := b.hasExecutor
		b.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.bridgeChannels(ctx, ch, b)
	if truthy(ch.Var("hangup_after_bridge")) && ctx.Err() == nil {
		ch.Hangup(CauseNormalClearing)
	}
	return nil
}

func apiUUIDRecord(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_record"].usage)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(args[1]) {
	case "start":
		if len(args) < 3 {
			return "", fmt.Errorf("usage: %s", apiCommands["uuid_record"].usage)
		}
		if err := appRecordSession(context.Background(), c, ch, args[2]); err != nil {
			return "", err
		}
	case "stop":
		ch.stopRecording()
	default:
		return "", fmt.Errorf("usage: %s", apiCommands["uuid_record"].usage)
	}
	return "+OK Success\n", nil
}

// uuid_broadcast plays a file to a channel in local media mode; bridged
// channels hear it instead of the other party while it plays.
func apiUUIDBroadcast(c *Core, args []string) (string, error) {
	ch, err := c.mustChannel(args, 2, apiCommands["uuid_broadcast"].usage)
	if err != nil {
		return "", err
	}
	path := c.resolveSound(ch, args[1])
	if path == "" {
		return "", fmt.Errorf("file not found: %s", args[1])
	}
	targets := []*Channel{ch}
	if len(args) > 2 {
		switch args[2] {
		case "bleg":
			if p := ch.Partner(); p != nil {
				targets = []*Channel{p}
			}
		case "both":
			if p := ch.Partner(); p != nil {
				targets = append(targets, p)
			}
		}
	}
	for _, t := range targets {
		go c.broadcast(t, path)
	}
	return "+OK Message sent\n", nil
}

func (c *Core) broadcast(ch *Channel, path string) {
	if ch.ep == nil {
		return
	}
	prev := ch.ep.Mode()
	ch.ep.SetMode(media.ModeLocal)
	_, _ = c.play(ch.ctx, ch, path, false)
	if !ch.IsHungUp() && prev == media.ModeRelay {
		ch.ep.SetMode(media.ModeRelay)
	}
}

func apiDomains(c *Core, _ []string) (string, error) {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	dir := c.config().Directory
	fmt.Fprintln(tw, "domain\tuuid\tusers\tregistrations")
	for _, d := range dir.List() {
		regs := 0
		for _, r := range c.reg.All() {
			if strings.EqualFold(r.Domain, d.Name) {
				regs++
			}
		}
		def := ""
		if strings.EqualFold(d.Name, dir.Domain) {
			def = " (default)"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%d\t%d\n", d.Name, def, d.UUID, len(d.Users), regs)
	}
	tw.Flush()
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// sofia

func apiSIP(c *Core, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: %s", apiCommands["sip"].usage)
	}
	switch strings.ToLower(args[0]) {
	case "xmlstatus":
		switch {
		case len(args) >= 2 && args[1] == "gateway":
			name := ""
			if len(args) >= 3 {
				name = args[2]
			}
			return c.sipGatewaysXML(name)
		case len(args) >= 4 && args[1] == "profile" && args[3] == "reg":
			return c.sipRegistrationsXML(args[4:]), nil
		}
		return c.sipStatusXML(), nil
	case "status":
		if len(args) >= 3 && args[1] == "gateway" {
			return c.sipGatewayStatus(args[2])
		}
		if len(args) >= 2 && args[1] == "gateway" {
			return apiShow(c, []string{"gateways"})
		}
		if len(args) >= 4 && args[1] == "profile" && args[3] == "reg" {
			return apiShow(c, []string{"registrations"})
		}
		return c.sipStatus(), nil
	case "profile":
		if len(args) < 3 {
			return "", fmt.Errorf("usage: %s", apiCommands["sip"].usage)
		}
		switch strings.ToLower(args[2]) {
		case "rescan", "restart", "start", "reload":
			if err := c.Reload(); err != nil {
				return "", err
			}
			return "+OK scan complete\n", nil
		case "killgw":
			if len(args) < 4 {
				return "", fmt.Errorf("usage: sofia profile <p> killgw <gateway>")
			}
			if gw := c.gateway(args[3]); gw != nil {
				c.removeGateway(gw)
				return "+OK gateway marked for deletion.\n", nil
			}
			return "", fmt.Errorf("invalid gateway")
		case "register", "unregister":
			if len(args) < 4 {
				return "", fmt.Errorf("usage: sofia profile <p> %s <gateway>", args[2])
			}
			gw := c.gateway(args[3])
			if gw == nil {
				return "", fmt.Errorf("invalid gateway")
			}
			if args[2] == "unregister" {
				_, _ = gw.register(0)
			} else {
				go func() { _, _ = gw.register(gw.cfg.Expire) }()
			}
			return "+OK\n", nil
		case "flush_inbound_reg":
			n := 0
			for _, b := range c.reg.All() {
				if len(args) > 3 && !strings.EqualFold(args[3], b.AOR()) && !strings.EqualFold(args[3], b.CallID) {
					continue
				}
				if removed := c.reg.remove(b.User, b.Domain, b.Contact); removed != nil {
					c.fireRegEvent("ringq::unregister", removed, "flushed")
					n++
				}
			}
			return fmt.Sprintf("+OK flushed %d registration(s)\n", n), nil
		case "check_sync", "reboot":
			// sofia profile <p> check_sync <user@domain>: the phones fetch
			// their configuration again (reboot: and restart).
			if len(args) < 4 {
				return "", fmt.Errorf("usage: sip profile <name> check_sync <user@domain>")
			}
			user, domain, _ := strings.Cut(args[3], "@")
			n := c.CheckSync(user, domain, args[2] == "reboot")
			return fmt.Sprintf("+OK sent to %d phone(s)\n", n), nil
		}
		return "+OK\n", nil
	case "global", "loglevel", "tracelevel":
		return "+OK\n", nil
	}
	return "", fmt.Errorf("usage: %s", apiCommands["sip"].usage)
}

func (c *Core) removeGateway(gw *Gateway) {
	c.mu.Lock()
	if c.gateways[gw.cfg.Name] == gw {
		delete(c.gateways, gw.cfg.Name)
	}
	c.mu.Unlock()
	go gw.stop()
}

func (c *Core) sipStatus() string {
	var b strings.Builder
	s := c.config().Settings
	host, port := s.SIPIP, c.stack.Port()
	fmt.Fprintf(&b, "%25s\t%s\t%40s\t%s\n", "Name", "Type", "Data", "State")
	fmt.Fprintln(&b, strings.Repeat("=", 99))
	fmt.Fprintf(&b, "%25s\t%s\t%40s\t%s\n", "internal", "profile", fmt.Sprintf("sip:mod_sofia@%s:%d", host, port), fmt.Sprintf("RUNNING (%d)", c.channelCount()))
	gws := c.gatewayList()
	sort.Slice(gws, func(i, j int) bool { return gws[i].cfg.Name < gws[j].cfg.Name })
	for _, g := range gws {
		fmt.Fprintf(&b, "%25s\t%s\t%40s\t%s\n", "external::"+g.cfg.Name, "gateway", "sip:"+g.cfg.Username+"@"+g.cfg.Proxy, g.State())
	}
	fmt.Fprintln(&b, strings.Repeat("=", 99))
	fmt.Fprintf(&b, "1 profile %d gateway(s)\n", len(gws))
	return b.String()
}

func (c *Core) sipGatewayStatus(name string) (string, error) {
	gw := c.gateway(name)
	if gw == nil {
		return "", fmt.Errorf("invalid gateway")
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Name    \t%s\nLabel   \t%s\nProfile \texternal\nScheme  \tDigest\nRealm   \t%s\nUsername\t%s\nPassword\t%s\nFrom    \t<sip:%s@%s>\nContact \t<sip:%s@...>\nProxy   \tsip:%s\nContext \t%s\nExpires \t%d\nFreq    \t%d\nState   \t%s\nStatus  \tUP\nLastErr \t%s\n",
		gw.cfg.Name, firstNonEmpty(gw.cfg.Label, gw.cfg.Name), gw.cfg.Realm, gw.cfg.Username, "yes",
		gw.cfg.FromUser, gw.cfg.FromDomain, gw.contactUser(), gw.cfg.Proxy, gw.cfg.Context, gw.cfg.Expire,
		gw.cfg.RetrySeconds, gw.state, gw.lastErr)
	return b.String(), nil
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sipRegistrationsXML renders "sofia xmlstatus profile internal reg"
// in the format the RingQ portal's registrations page parses.
func (c *Core) sipRegistrationsXML(filter []string) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<profile>\n  <registrations>\n")
	for _, r := range c.reg.All() {
		if len(filter) > 0 && !strings.EqualFold(filter[0], r.User) && !strings.EqualFold(filter[0], r.AOR()) {
			continue
		}
		tr := strings.ToUpper(r.Src.Transport)
		fmt.Fprintf(&b, "    <registration>\n      <call-id>%s</call-id>\n      <user>%s</user>\n      <contact>%s</contact>\n      <agent>%s</agent>\n      <status>%s</status>\n      <ping-status>Reachable</ping-status>\n      <ping-time>0.00</ping-time>\n      <host>%s</host>\n      <network-ip>%s</network-ip>\n      <network-port>%d</network-port>\n      <sip-auth-user>%s</sip-auth-user>\n      <sip-auth-realm>%s</sip-auth-realm>\n      <mwi-account>%s</mwi-account>\n    </registration>\n",
			xmlText(r.CallID), xmlText(r.AOR()), xmlText("\""+r.User+"\" <"+r.Contact+">"), xmlText(r.UserAgent),
			xmlText(fmt.Sprintf("Registered(%s)(unknown) EXP(%s) EXPSECS(%d)", tr, r.Expires.Format("2006-01-02 15:04:05"), int(time.Until(r.Expires).Seconds()))),
			xmlText(c.events.hostname), r.Src.AP.Addr(), r.Src.AP.Port(), xmlText(r.User), xmlText(r.Domain), xmlText(r.AOR()))
	}
	b.WriteString("  </registrations>\n</profile>\n")
	return b.String()
}

// sipGatewaysXML renders "sofia xmlstatus gateway [name]".
func (c *Core) sipGatewaysXML(name string) (string, error) {
	gws := c.gatewayList()
	if name != "" {
		gw := c.gateway(name)
		if gw == nil {
			return "", fmt.Errorf("invalid gateway")
		}
		gws = []*Gateway{gw}
	}
	sort.Slice(gws, func(i, j int) bool { return gws[i].cfg.Name < gws[j].cfg.Name })
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	if name == "" {
		b.WriteString("<gateways>\n")
	}
	for _, g := range gws {
		g.mu.Lock()
		state, cfg := g.state, g.cfg
		g.mu.Unlock()
		status := "UP"
		if state == GwFailed {
			status = "DOWN"
		}
		fmt.Fprintf(&b, "<gateway>\n  <name>%s</name>\n  <profile>external</profile>\n  <scheme>Digest</scheme>\n  <realm>%s</realm>\n  <username>%s</username>\n  <password>yes</password>\n  <from>&lt;sip:%s@%s&gt;</from>\n  <contact>&lt;sip:%s@%s&gt;</contact>\n  <exten>%s</exten>\n  <to>sip:%s@%s</to>\n  <proxy>sip:%s</proxy>\n  <context>%s</context>\n  <expires>%d</expires>\n  <freq>%d</freq>\n  <ping>0</ping>\n  <pingfreq>0</pingfreq>\n  <state>%s</state>\n  <status>%s</status>\n  <uptime-usec>%d</uptime-usec>\n  <calls-in>0</calls-in>\n  <calls-out>0</calls-out>\n  <failed-calls-in>0</failed-calls-in>\n  <failed-calls-out>0</failed-calls-out>\n</gateway>\n",
			xmlText(cfg.Name), xmlText(cfg.Realm), xmlText(cfg.Username), xmlText(cfg.FromUser), xmlText(cfg.FromDomain),
			xmlText(g.contactUser()), xmlText(c.config().Settings.SIPIP), xmlText(cfg.Username), xmlText(cfg.Username),
			xmlText(cfg.Realm), xmlText(cfg.Proxy), xmlText(cfg.Context), cfg.Expire, cfg.RetrySeconds, state, status,
			time.Since(c.started).Microseconds())
	}
	if name == "" {
		b.WriteString("</gateways>\n")
	}
	return b.String(), nil
}

func (c *Core) sipStatusXML() string {
	s := c.config().Settings
	var b strings.Builder
	fmt.Fprintf(&b, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<profiles>\n<profile>\n  <name>internal</name>\n  <type>profile</type>\n  <data>sip:mod_sofia@%s:%d</data>\n  <state>RUNNING (%d)</state>\n</profile>\n",
		xmlText(s.SIPIP), c.stack.Port(), c.channelCount())
	for _, g := range c.gatewayList() {
		fmt.Fprintf(&b, "<gateway>\n  <name>external::%s</name>\n  <type>gateway</type>\n  <data>sip:%s@%s</data>\n  <state>%s</state>\n</gateway>\n",
			xmlText(g.cfg.Name), xmlText(g.cfg.Username), xmlText(g.cfg.Proxy), g.State())
	}
	b.WriteString("</profiles>\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// compat: what the loaded dialplan uses that RingQswitch lacks

func apiCompat(c *Core, _ []string) (string, error) {
	return c.CompatReport(), nil
}

// CompatReport lists dialplan applications and expansion functions used in
// the loaded configuration that are unsupported or accepted as no-ops.
func (c *Core) CompatReport() string {
	cfg := c.config()
	unknown := map[string][]string{}
	noop := map[string][]string{}
	unsupported := map[string][]string{}
	funcs := map[string][]string{}
	known := knownFuncs()
	noopSet := map[string]bool{}
	for _, n := range noopApps {
		noopSet[n] = true
	}
	note := func(m map[string][]string, key, where string) {
		if len(m[key]) < 5 {
			m[key] = append(m[key], where)
		}
	}
	for _, ctx := range cfg.Dialplan.Contexts {
		for _, ext := range ctx.Extensions {
			where := ctx.Name + "/" + ext.Name
			var visit func(cs []*dialplan.Condition)
			visit = func(cs []*dialplan.Condition) {
				for _, cond := range cs {
					texts := []string{cond.Field}
					for _, list := range [][]dialplan.Action{cond.Actions, cond.AntiActions} {
						for _, a := range list {
							texts = append(texts, a.Data)
							fn := c.apps[a.App]
							switch {
							case fn == nil:
								note(unknown, a.App, where)
							case noopSet[a.App]:
								note(noop, a.App, where)
							case isUnsupported(a.App):
								note(unsupported, a.App, where)
							case a.App == "lua":
								if !luaSupported(a.Data) {
									note(unsupported, "lua "+a.Data, where)
								}
							}
						}
					}
					for _, t := range texts {
						for _, f := range exprFuncs(t) {
							if !known[f] {
								note(funcs, f, where)
							}
						}
					}
					visit(cond.Nested)
				}
			}
			visit(ext.Conditions)
		}
	}
	var b strings.Builder
	section := func(title string, m map[string][]string) {
		if len(m) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %-28s used in %s\n", k, strings.Join(m[k], ", "))
		}
	}
	section("Unknown applications (skipped with a warning)", unknown)
	section("Applications not supported yet (the call continues past them)", unsupported)
	section("Unknown ${functions} (expand to empty)", funcs)
	section("Accepted as no-ops", noop)
	if b.Len() == 0 {
		return "+OK every application and function in the dialplan is supported\n"
	}
	return b.String()
}

func isUnsupported(app string) bool {
	switch app {
	case "say", "phrase", "speak", "voicemail", "callcenter", "conference", "fifo", "valet_park",
		"intercept", "eavesdrop", "att_xfer", "system":
		return true
	}
	return false
}

func luaSupported(data string) bool {
	f := strings.Fields(data)
	if len(f) == 0 {
		return true
	}
	name := strings.TrimSuffix(filepath.Base(f[0]), ".lua")
	if name == "app" && len(f) > 1 {
		name = f[1]
	}
	switch name {
	case "failure_handler", "ring_groups", "ring_group", "ivr_menu", "ivr_menus":
		return true
	}
	return false
}

func knownFuncs() map[string]bool {
	m := map[string]bool{}
	for _, n := range []string{"user_exists", "user_data", "domain_exists", "cond", "strftime", "strepoch", "epoch",
		"create_uuid", "uuid", "regex", "sofia_contact", "expr", "eval", "global_getvar", "uuid_getvar",
		"uuid_exists", "hash", "db", "url_encode", "url_decode", "md5", "toupper", "tolower",
		"sofia_count_reg", "count_reg", "registered", "user_registered"} {
		m[n] = true
	}
	return m
}

// exprFuncs returns the function names called in ${...} references.
func exprFuncs(s string) []string {
	var out []string
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			return out
		}
		s = s[i+2:]
		end := strings.IndexAny(s, " (}")
		if end > 0 && end < len(s) && (s[end] == ' ' || s[end] == '(') {
			out = append(out, strings.ToLower(s[:end]))
		}
	}
}
