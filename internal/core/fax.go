package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/fax"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sdp"
)

// rxfax <file> and txfax <file>: fax on G.711 audio, or over T.38 (UDPTL)
// when the far end re-INVITEs to it (fax_enable_t38=false refuses that),
// with the channel variables and events dialplans and event socket
// clients expect, so they keep working.
//
// The caller sends and the answerer receives: txfax belongs on a call we
// placed (originate ... &txfax(file)), rxfax on a call we answered.
//
// Input variables: fax_ident (our station number), fax_header (the
// header line, usually the company name), fax_verbose, fax_timeout
// (seconds). Results: fax_success, fax_result_code, fax_result_text,
// fax_document_transferred_pages, fax_document_total_pages,
// fax_remote_station_id, fax_local_station_id, fax_transfer_rate,
// fax_image_resolution, fax_bad_rows, fax_ecm_used.

const faxMaxDuration = 60 * time.Minute

func appRxFax(ctx context.Context, c *Core, ch *Channel, data string) error {
	path := strings.TrimSpace(data)
	if path == "" {
		return errors.New("rxfax: missing file")
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	ident := ch.Var("fax_ident")
	if ident == "" {
		ident = ch.Var("destination_number")
	}
	opt := c.faxOptions(ch, ident, "rx")
	res := c.runFax(ctx, ch, opt, func(o fax.Options) *fax.Terminal { return fax.NewReceiver(o) })
	if len(res.Received) > 0 {
		if err := writeFaxFile(path, res.Received); err != nil {
			ch.log.Error("rxfax: cannot save the fax", "path", path, "err", err)
			res.Success, res.Code, res.Text = false, fax.ErrFile, "Cannot save the received fax: "+err.Error()
		}
	}
	c.faxDone(ch, "rxfax", ident, path, res)
	return nil
}

func appTxFax(ctx context.Context, c *Core, ch *Channel, data string) error {
	path := strings.TrimSpace(data)
	if path == "" {
		return errors.New("txfax: missing file")
	}
	ident := ch.Var("fax_ident")
	if ident == "" {
		ident = ch.Var("caller_id_number")
	}
	pages, err := fax.LoadDocument(path)
	if err != nil {
		ch.log.Error("txfax: cannot read the document", "path", path, "err", err)
		c.faxDone(ch, "txfax", ident, path, fax.Result{Code: fax.ErrFile, Text: fax.ResultText(fax.ErrFile) + ": " + err.Error()})
		return nil
	}
	if err := startMedia(ch, false); err != nil {
		return err
	}
	opt := c.faxOptions(ch, ident, "tx")
	opt.Header = ch.Var("fax_header")
	res := c.runFax(ctx, ch, opt, func(o fax.Options) *fax.Terminal { return fax.NewSender(pages, o) })
	c.faxDone(ch, "txfax", ident, path, res)
	return nil
}

func (c *Core) faxOptions(ch *Channel, ident, dir string) fax.Options {
	opt := fax.Options{Ident: ident}
	opt.Log = func(format string, args ...any) { // a fax says little: always in the log
		ch.log.Info(fmt.Sprintf(format, args...))
	}
	event := "spandsp::txfaxpageresult"
	if dir == "rx" {
		event = "spandsp::rxfaxpageresult"
	}
	opt.OnPage = func(page, total int) {
		ch.SetVar("fax_document_transferred_pages", strconv.Itoa(page))
		e := NewEvent(EvCustom, event)
		ch.eventHeaders(e, false)
		e.Add("fax-document-transferred-pages", strconv.Itoa(page))
		e.Add("fax-document-total-pages", strconv.Itoa(total))
		c.events.Publish(e)
	}
	return opt
}

// runFax connects a terminal (made by mk) to the channel's audio:
// received frames go in as they arrive, and the send clock (20 ms) paces
// what goes out. When the far end switches the call to T.38, a new
// terminal takes over on UDPTL (the fax starts over: gateways switch at
// the first tones, before anything was exchanged).
func (c *Core) runFax(ctx context.Context, ch *Channel, opt fax.Options, mk func(fax.Options) *fax.Terminal) fax.Result {
	limit := faxMaxDuration
	if n, err := strconv.Atoi(ch.Var("fax_timeout")); err == nil && n > 0 {
		limit = time.Duration(n) * time.Second
	}
	fctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ch.ep.FlushInput()
	var mu sync.Mutex
	term := mk(opt)
	l := &faxListener{}
	var udptl *fax.UDPTL // once the call is T.38
	ch.mu.Lock()
	ch.faxT38 = func(remote netip.AddrPort, offer sdp.T38) (sdp.T38, bool) {
		mu.Lock()
		defer mu.Unlock()
		if term.Done() {
			return sdp.T38{}, false
		}
		ans := faxT38Answer(offer)
		if udptl == nil {
			o := opt
			o.T38, o.T38Version = true, ans.Version
			redundancy := 0
			if ans.UDPEC == "t38UDPRedundancy" {
				redundancy = 3
			} else {
				o.T38Repeat = 3
			}
			udptl = fax.NewUDPTL(redundancy, offer.MaxDatagram)
			term = mk(o)
			l.t38 = true
			ch.log.Info("fax: the far end switched the call to T.38: the fax goes on over UDPTL",
				"version", ans.Version, "error_correction", firstNonEmpty(ans.UDPEC, "none"), "max_datagram", offer.MaxDatagram, "remote", remote)
		}
		ch.ep.SetRemote(remote, netip.AddrPortFrom(remote.Addr(), remote.Port()+1))
		ch.ep.SetRaw(func(d []byte) {
			mu.Lock()
			defer mu.Unlock()
			ifps, err := udptl.Receive(d)
			if err != nil {
				l.t38Bad++
				return
			}
			l.t38In++
			for _, p := range ifps {
				term.ReceiveIFP(p)
			}
		})
		return ans, true
	}
	ch.mu.Unlock()
	defer func() {
		ch.mu.Lock()
		ch.faxT38 = nil
		ch.mu.Unlock()
	}()
	go func() {
		for {
			fr, err := ch.ep.ReadFrame(fctx)
			if err != nil {
				return
			}
			mu.Lock()
			if udptl == nil {
				l.hear(fr)
				term.Receive(fr)
			}
			done := term.Done()
			mu.Unlock()
			if done {
				return
			}
		}
	}()
	for tick := 1; ; tick++ {
		out := make([]int16, media.FrameSamples)
		mu.Lock()
		term.Transmit(out)
		if udptl != nil {
			for _, p := range term.TakeIFP() {
				ch.ep.WriteRaw(udptl.Packet(p))
				l.t38Out++
			}
		} else {
			l.send(out)
		}
		done := term.Done()
		if tick%faxReportEvery == 0 {
			ch.log.Info("fax: " + l.report())
		}
		mu.Unlock()
		if err := ch.ep.WriteFrame(fctx, out); err != nil || done {
			break
		}
	}
	mu.Lock()
	if !term.Done() { // the call ended (or ran too long) first
		term.Hangup()
	}
	res := term.Result()
	stats := term.Stats()
	if udptl != nil {
		l.t38Lost, l.t38Recovered = udptl.Lost, udptl.Recover
	}
	mu.Unlock()
	ch.log.Info("fax: "+faxStatsLine(stats)+l.t38Summary(), "packets", l.total)
	if !res.Success || truthy(ch.Var("fax_trace")) {
		mu.Lock()
		c.keepTrace(ch, l)
		mu.Unlock()
	}
	if !ch.IsHungUp() { // let the last frames (DCN) go out
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	return res
}

// faxT38Answer is our side of a T.38 offer: its version (up to 3), the
// V.29 rates, transferred TCF, and redundancy when it can use any error
// correction.
func faxT38Answer(offer sdp.T38) sdp.T38 {
	ans := sdp.T38{Version: min(offer.Version, 3), MaxBitRate: 9600, RateManagement: "transferredTCF",
		MaxBuffer: 2000, MaxDatagram: 400}
	switch strings.ToLower(offer.UDPEC) {
	case "t38udpredundancy", "t38udpfec":
		ans.UDPEC = "t38UDPRedundancy"
	}
	return ans
}

func t38Status(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func writeFaxFile(path string, pages []*fax.Page) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		err = fax.WritePDF(f, pages)
	} else {
		err = fax.WriteTIFF(f, pages)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// faxDone sets the result variables and fires spandsp's result event.
func (c *Core) faxDone(ch *Channel, app, ident, path string, res fax.Result) {
	if res.Text == "" {
		res.Text = fax.ResultText(res.Code)
	}
	success := "0"
	if res.Success {
		success = "1"
	}
	resolution := "204x98"
	if res.Fine {
		resolution = "204x196"
	}
	vars := [][2]string{
		{"fax_success", success},
		{"fax_result_code", strconv.Itoa(res.Code)},
		{"fax_result_text", res.Text},
		{"fax_ecm_used", "off"},
		{"fax_t38_status", t38Status(res.T38)},
		{"fax_local_station_id", ident},
		{"fax_remote_station_id", res.RemoteID},
		{"fax_document_transferred_pages", strconv.Itoa(res.Pages)},
		{"fax_document_total_pages", strconv.Itoa(res.TotalPages)},
		{"fax_image_resolution", resolution},
		{"fax_transfer_rate", strconv.Itoa(res.Rate)},
		{"fax_bad_rows", strconv.Itoa(res.BadRows)},
	}
	for _, v := range vars {
		ch.SetVar(v[0], v[1])
	}
	if res.Success {
		ch.log.Info(app+": fax complete", "pages", res.Pages, "remote", res.RemoteID, "rate", res.Rate, "file", path)
	} else {
		ch.log.Warn(app+": fax failed", "result", res.Text, "code", res.Code, "pages", res.Pages, "file", path)
	}
	e := NewEvent(EvCustom, "spandsp::"+app+"result")
	ch.eventHeaders(e, true)
	for _, v := range vars {
		e.Add(strings.ReplaceAll(v[0], "_", "-"), v[1])
	}
	e.Add("fax-filename", path)
	c.events.Publish(e)
}
