package core

import (
	"encoding/csv"
	"log/slog"
	"os"
	"path/filepath"
)

var cdrColumns = []string{
	"uuid", "direction", "caller_id_name", "caller_id_number", "destination_number",
	"context", "start_stamp", "answer_stamp", "end_stamp", "duration", "billsec",
	"hangup_cause", "bridge_uuid", "sip_call_id", "network_addr", "user_name", "gateway_name",
}

// cdrWriter appends one CSV line per finished channel.
type cdrWriter struct {
	f    *os.File
	w    *csv.Writer
	sub  *Subscription
	log  *slog.Logger
	done chan struct{}
}

func newCDRWriter(path string, bus *EventBus, log *slog.Logger) (*cdrWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	w := &cdrWriter{f: f, w: csv.NewWriter(f), log: log, done: make(chan struct{})}
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		_ = w.w.Write(cdrColumns)
		w.w.Flush()
	}
	w.sub = bus.Subscribe(4096, func(e *Event) bool { return e.Name == EvChannelHangupComplete })
	go w.run()
	return w, nil
}

func (w *cdrWriter) run() {
	defer close(w.done)
	for e := range w.sub.C {
		row := make([]string, len(cdrColumns))
		for i, col := range cdrColumns {
			row[i] = e.Get("variable_" + col)
		}
		if err := w.w.Write(row); err != nil {
			w.log.Error("cdr write failed", "err", err)
		}
		w.w.Flush()
	}
}

func (w *cdrWriter) close() {
	// After Close no publisher can be sending, so closing C is safe and
	// lets run drain whatever is still buffered.
	w.sub.Close()
	close(w.sub.C)
	<-w.done
	w.w.Flush()
	w.f.Close()
}
