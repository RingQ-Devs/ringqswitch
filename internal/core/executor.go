package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/Cal4Care-Developers/RingQswitch/internal/dialplan"
)

// App is a dialplan application. ctx is cancelled when the channel hangs up
// or is transferred.
type App func(ctx context.Context, c *Core, ch *Channel, data string) error

const maxTransfers = 20

// runExecutor routes the channel through the dialplan and runs the resulting
// applications until the channel hangs up or runs out of actions.
func (c *Core) runExecutor(ch *Channel) { c.runExecutorWith(ch, nil) }

// runExecutorWith runs the given actions first (used by originate with
// &app(...)); after them, or after a transfer, the dialplan is hunted.
func (c *Core) runExecutorWith(ch *Channel, initial []dialplan.Action) {
	done := make(chan struct{})
	ch.mu.Lock()
	ch.hasExecutor = true
	ch.execDone = done
	ch.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			ch.log.Error("panic in call executor", "panic", r, "stack", string(debug.Stack()))
			ch.Hangup(CauseInterworking)
		}
		ch.mu.Lock()
		ch.hasExecutor = false
		if ch.execDone == done {
			ch.execDone = nil
		}
		ch.mu.Unlock()
		close(done)
	}()
	ch.flowStart()
	actions := initial
	for {
		if ch.IsHungUp() {
			return
		}
		if actions == nil {
			actions = ch.takeQueuedActions()
		}
		if actions == nil {
			ch.setState(CSRouting)
			context, dest := ch.Var("context"), ch.Var("destination_number")
			logAt(ch.log, slog.LevelInfo, fmt.Sprintf("Processing %s <%s>->%s in context %s", ch.Var("caller_id_name"), ch.Var("caller_id_number"), dest, context))
			actions = c.officeHours(ch, context, dest)
		}
		if actions == nil {
			context, dest := ch.Var("context"), ch.Var("destination_number")
			found, matched, err := c.config().Dialplan.Hunt(context, ch, c.callNow(ch))
			if err != nil || len(found) == 0 {
				ch.log.Warn("no route", "context", context, "destination", dest, "err", err)
				c.noRoute(ch, dest)
				ch.Hangup(CauseNoRouteDestination)
				return
			}
			ch.log.Debug("dialplan", "context", context, "destination", dest, "extensions", matched)
			actions = found
		}
		ch.setState(CSExecute)
		transferred := false
		for _, a := range actions {
			if ch.IsHungUp() {
				return
			}
			if t := ch.applyTransfer(); t != nil {
				if t.adopt {
					return
				}
				transferred = true
				break
			}
			app := c.apps[a.App]
			if app == nil {
				ch.log.Warn("unknown application", "app", a.App)
				continue
			}
			data := ch.Expand(a.Data)
			ch.SetVar("last_app", a.App)
			ch.SetVar("last_arg", data)
			ctx := ch.beginApp(a.App)
			if ch.Tracing() {
				ch.traceRaw(fmt.Sprintf("EXECUTE [depth=0] %s %s(%s)", ch.name(), a.App, data))
			}
			ch.fire(EvChannelExecute, "Application", a.App, "Application-Data", data)
			err := app(ctx, c, ch, data)
			ch.endApp()
			ch.fire(EvChannelExecuteDone, "Application", a.App, "Application-Data", data)
			if err != nil && !errors.Is(err, context.Canceled) && !ch.IsHungUp() {
				ch.log.Warn("application failed", "app", a.App, "data", data, "err", err)
			}
			if t := ch.applyTransfer(); t != nil {
				if t.adopt {
					return
				}
				transferred = true
				break
			}
		}
		if !transferred {
			ch.Hangup(endCause(ch))
			return
		}
		ch.mu.Lock()
		ch.transfers++
		n := ch.transfers
		ch.mu.Unlock()
		if n > maxTransfers {
			ch.log.Warn("transfer loop detected")
			ch.Hangup(CauseExchangeRoutingError)
			return
		}
		actions = nil
	}
}

func (ch *Channel) beginApp(name string) context.Context {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ctx, cancel := context.WithCancel(ch.ctx)
	if ch.pendingTransfer != nil {
		cancel()
	}
	ch.appCancel = cancel
	ch.app = name
	return ctx
}

func (ch *Channel) endApp() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.appCancel != nil {
		ch.appCancel()
		ch.appCancel = nil
	}
	ch.app = ""
}

// applyTransfer consumes a pending transfer request.
func (ch *Channel) applyTransfer() *transferReq {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	t := ch.pendingTransfer
	if t == nil {
		return nil
	}
	ch.pendingTransfer = nil
	switch {
	case t.adopt:
	case t.actions != nil:
		ch.queuedActions = t.actions
	default:
		ch.vars["destination_number"] = t.dest
		if t.context != "" {
			ch.vars["context"] = t.context
		}
		ch.log.Info("transfer", "destination", t.dest, "context", ch.vars["context"])
	}
	return t
}

func (ch *Channel) takeQueuedActions() []dialplan.Action {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	a := ch.queuedActions
	ch.queuedActions = nil
	return a
}

// transfer moves a channel to another dialplan destination. A channel that
// is running the dialplan is interrupted; a bridged outbound leg is detached
// from its bridge and gets its own executor.
func (c *Core) transfer(ch *Channel, dest, dpContext string) {
	ch.mu.Lock()
	if ch.hungup {
		ch.mu.Unlock()
		return
	}
	if dpContext == "" {
		dpContext = ch.vars["context"]
	}
	ch.flowLocked("route", "Routed to "+dest)
	if ch.hasExecutor {
		ch.pendingTransfer = &transferReq{dest: dest, context: dpContext}
		if ch.appCancel != nil {
			ch.appCancel()
		}
		ch.mu.Unlock()
		return
	}
	ch.vars["destination_number"] = dest
	if dpContext == "" {
		dpContext = "default"
	}
	ch.vars["context"] = dpContext
	ch.hasExecutor = true
	ctl := ch.bctl
	ch.mu.Unlock()
	if ctl != nil {
		ctl.Detach()
	}
	go c.runExecutor(ch)
}

// endCause is the cause used when the dialplan runs out of actions: an
// unanswered call whose last bridge failed ends with that failure (the
// caller hears busy rather than a generic error).
func endCause(ch *Channel) Cause {
	if ch.IsAnswered() {
		return CauseNormalClearing
	}
	if d := ch.Var("originate_disposition"); d != "" && d != "SUCCESS" {
		if c := ParseCause(d); c != CauseNormalClearing {
			return c
		}
	}
	return CauseNormalClearing
}
