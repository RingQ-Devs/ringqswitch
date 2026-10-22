package core

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/logbus"
)

// Console log lines, the ones the console shows ("New Channel ...",
// "Dialplan: ... Regex (PASS) ...", "EXECUTE ...", "Hangup ..."): rq_cli
// follows them with "log 7".

// logAt logs msg at level with the file and line of its caller.
func logAt(l *slog.Logger, level slog.Level, msg string, args ...any) {
	logSkip(l, 3, level, msg, args...)
}

// logSkip logs with the file and line of a caller (3: the caller of
// the function that calls logSkip).
func logSkip(l *slog.Logger, skip int, level slog.Level, msg string, args ...any) {
	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(skip, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.Add(args...)
	_ = l.Handler().Handle(ctx, r)
}

func (c *Core) notice(msg string, args ...any) { logSkip(c.log, 3, logbus.LevelNotice, msg, args...) }

func (ch *Channel) notice(msg string, args ...any) {
	logSkip(ch.log, 3, logbus.LevelNotice, msg, args...)
}

func (ch *Channel) debugf(format string, args ...any) {
	logSkip(ch.log, 3, slog.LevelDebug, fmt.Sprintf(format, args...))
}

// Tracing says whether someone reads debug lines (the dialplan trace is
// only made then).
func (ch *Channel) Tracing() bool { return ch.log.Enabled(context.Background(), slog.LevelDebug) }

// Trace writes one "Dialplan:" line.
func (ch *Channel) Trace(line string) {
	logSkip(ch.log, 3, slog.LevelDebug, "Dialplan: "+ch.name()+" "+line, logbus.RawKey, true)
}

// traceRaw writes a line as it is ("EXECUTE [depth=0] ...").
func (ch *Channel) traceRaw(line string) {
	logSkip(ch.log, 3, slog.LevelDebug, line, logbus.RawKey, true)
}
