package core

import (
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
)

// Domain time and the global office hours of the Language & office hours
// page: calls from outside are routed in their company's time zone, and
// while the company is closed (a holiday, a break, outside office hours)
// they get the page's action instead of their DID's destination.

var zoneCache sync.Map // name: *time.Location

func loadZone(name string) *time.Location {
	if v, ok := zoneCache.Load(name); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	zoneCache.Store(name, loc)
	return loc
}

// domainNow is the time in a domain's time zone (the server's when it has
// none).
func (c *Core) domainNow(domain string) time.Time {
	return c.config().Directory.Find(domain).Now()
}

// callNow is the time a call is routed by: its timezone variable, else
// its domain's (or its context's) time zone, else the server's.
func (c *Core) callNow(ch *Channel) time.Time {
	if tz := ch.Var("timezone"); tz != "" {
		if loc := loadZone(tz); loc != nil {
			return time.Now().In(loc)
		}
	}
	dir := c.config().Directory
	for _, name := range []string{ch.Var("domain_name"), ch.Var("context")} {
		if d := dir.Find(name); d != nil && d.Loc != nil {
			return time.Now().In(d.Loc)
		}
	}
	return time.Now()
}

// officeHours returns what to do instead of routing a call from outside
// that reaches its company while it is closed (nil: route it). A call is
// looked at once, when it first reaches its company's context.
func (c *Core) officeHours(ch *Channel, context, dest string) []dialplan.Action {
	if ch.Direction != DirectionInbound || ch.Var("sip_authorized") == "true" || ch.Var("ringq_hours_checked") != "" ||
		ch.Var("is_loopback") == "true" {
		return nil
	}
	dom := c.config().Directory.Find(context)
	if dom == nil {
		return nil // still in the public context
	}
	ch.SetVar("ringq_hours_checked", "true")
	cl := dom.Hours.Check(dom.Now(), dest)
	if cl == nil {
		return nil
	}
	ch.SetVar("ringq_closed", cl.Why)
	label := map[string]string{"holiday": "Closed for a holiday", "break": "Break time", "closed": "Outside office hours"}[cl.Why]
	if cl.Name != "" {
		label += " (" + cl.Name + ")"
	}
	ch.log.Info("company closed: office hours action", "why", cl.Why, "holiday", cl.Name, "destination", dest, "action", cl.Action)
	ch.flow("closed", label)
	var acts []dialplan.Action
	if cl.Why == "holiday" {
		sound := ""
		if cl.Greeting != "" {
			sound = c.resolveSound(ch, cl.Greeting)
		}
		if sound == "" {
			sound = c.prompt(ch, "holiday-closed")
		}
		acts = append(acts, dialplan.Action{App: "answer"}, dialplan.Action{App: "sleep", Data: "500"})
		if sound != "" {
			acts = append(acts, dialplan.Action{App: "playback", Data: sound})
		}
		if cl.Action == "" {
			cl.Action = "hangup:NORMAL_CLEARING"
		}
	}
	app, data, _ := strings.Cut(cl.Action, ":")
	if app = strings.TrimSpace(app); app != "" {
		acts = append(acts, dialplan.Action{App: app, Data: strings.TrimSpace(data)})
	}
	return acts
}
