package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/fax"
)

// eFax: faxes sent from the portal and received on fax extensions, kept
// in the RingQ fax engine's tables (fax_jobs, fax_domain_settings).
//
// Sending: a job is queued; the worker calls the number through the trunk
// of the DID it is sent from (txfax) and files the result. Receiving: a
// DID assigned to a fax extension (Call Flow, Others) is answered as a fax
// machine (rxfax); what arrives is a PDF in the inbox.
//
//	GET    /api/d/{domain}/fax                       counts, numbers, settings
//	GET    /api/d/{domain}/fax/jobs?direction=&status=&limit=
//	POST   /api/d/{domain}/fax/send?to=&from=&name=  body: the document (PDF, TIFF, image)
//	GET    /api/d/{domain}/fax/jobs/{id}/file        the fax (PDF) or the document sent
//	POST   /api/d/{domain}/fax/jobs/{id}/retry
//	DELETE /api/d/{domain}/fax/jobs/{id}
//	PUT    /api/d/{domain}/fax/settings              {fax_ident, fax_header}
//
// Statuses: out queued, dialing, sending, then sent or failed; in
// receiving, then received or failed.

const (
	faxMaxCalls    = 4        // faxes sent at once
	faxRingSeconds = 60       // how long a fax call rings
	faxMaxUpload   = 20 << 20 // bytes
	faxMaxTo       = 20       // numbers one document goes to
)

var reFaxNumber = regexp.MustCompile(`^\+?[0-9]{3,20}$`)

func (s *Server) faxReady() error {
	if s.store == nil || !s.store.HasTable("fax_jobs") || !s.store.HasTable("fax_domain_settings") {
		return badRequest("the eFax tables are missing: restart RingQswitch once to add them")
	}
	return nil
}

func (s *Server) faxDir() string { return s.sw.Config().Settings.FaxDir }

func (s *Server) faxAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if err := s.faxReady(); err != nil {
		return err
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		return s.faxOverview(ctx, w, dom)
	case part(parts, 0) == "jobs" && len(parts) == 1 && r.Method == http.MethodGet:
		return s.faxJobs(ctx, w, r, dom)
	case part(parts, 0) == "send" && r.Method == http.MethodPost:
		return s.faxSend(ctx, w, r, p, dom)
	case part(parts, 0) == "settings" && r.Method == http.MethodPut:
		var b struct {
			FaxIdent  string `json:"fax_ident"`
			FaxHeader string `json:"fax_header"`
		}
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		b.FaxIdent, b.FaxHeader = strings.TrimSpace(b.FaxIdent), strings.TrimSpace(b.FaxHeader)
		if len(b.FaxIdent) > 20 {
			return badRequest("station id: 20 characters at most (fax machines show no more)")
		}
		if len(b.FaxHeader) > 60 {
			return badRequest("page header: 60 characters at most")
		}
		if _, err := s.store.Pool().Exec(ctx, `insert into fax_domain_settings (domain_uuid, fax_ident, fax_header, updated_at)
			values ($1::uuid, $2, $3, $4) on conflict (domain_uuid) do update set fax_ident = excluded.fax_ident,
			fax_header = excluded.fax_header, updated_at = excluded.updated_at`, dom.uuid, nullIfEmpty(b.FaxIdent), nullIfEmpty(b.FaxHeader), epoch()); err != nil {
			return err
		}
		s.log.Info("efax: settings saved", "domain", dom.name, "by", p.key())
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil
	case part(parts, 0) == "jobs" && reUUID.MatchString(part(parts, 1)):
		id := parts[1]
		job, err := s.faxJob(ctx, dom, id)
		if err != nil {
			return err
		}
		switch {
		case part(parts, 2) == "file" && r.Method == http.MethodGet:
			return s.faxFile(w, r, job)
		case part(parts, 2) == "trace" && r.Method == http.MethodGet:
			// The call's audio, kept when a fax fails (for troubleshooting).
			side := r.URL.Query().Get("side")
			if side != "sent" {
				side = "heard"
			}
			path := s.faxTracePath(dom, job["call_uuid"], side)
			f, err := os.Open(path)
			if path == "" || err != nil {
				return notFound("audio")
			}
			defer f.Close()
			st, _ := f.Stat()
			w.Header().Set("Content-Type", "audio/wav")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fax-%s-%s.wav"`, job["call_uuid"], side))
			http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
			return nil
		case part(parts, 2) == "retry" && r.Method == http.MethodPost:
			if job["direction"] != "out" || job["status"] != "failed" {
				return badRequest("only a fax that failed to send can be sent again")
			}
			if _, err := os.Stat(job["file_path"]); err != nil {
				return badRequest("the document of this fax is gone: send it again")
			}
			if _, err := s.store.Pool().Exec(ctx, `update fax_jobs set status = 'queued', error_reason = null, pages = 0, call_uuid = null,
				updated_at = $3 where id = $1::uuid and domain_uuid = $2::uuid`, id, dom.uuid, epoch()); err != nil {
				return err
			}
			s.log.Info("efax: sending again", "job", id, "to", job["to_number"], "domain", dom.name, "by", p.key())
			writeJSON(w, map[string]string{"ok": "queued"})
			return nil
		case len(parts) == 2 && r.Method == http.MethodDelete:
			if faxBusy(job["status"]) {
				return badRequest("this fax is in progress: wait for it to finish")
			}
			if _, err := s.store.Pool().Exec(ctx, `delete from fax_jobs where id = $1::uuid and domain_uuid = $2::uuid`, id, dom.uuid); err != nil {
				return err
			}
			if f := job["file_path"]; f != "" && strings.HasPrefix(filepath.Clean(f), filepath.Clean(s.faxDir())+string(filepath.Separator)) {
				os.Remove(f) // only files eFax keeps itself
			}
			s.log.Info("efax: deleted", "job", id, "domain", dom.name, "by", p.key())
			writeJSON(w, map[string]string{"ok": "deleted"})
			return nil
		}
	}
	return errMethod
}

func faxBusy(status string) bool {
	switch status {
	case "queued", "dialing", "sending", "receiving":
		return true
	}
	return false
}

func epoch() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

const faxJobJSON = `jsonb_build_object('id', id, 'direction', direction, 'from_number', coalesce(from_number, ''),
	'to_number', coalesce(to_number, ''), 'pages', coalesce(pages, 0), 'status', status, 'error_reason', coalesce(error_reason, ''),
	'call_uuid', coalesce(call_uuid, ''), 'created_at', created_at, 'updated_at', updated_at, 'file_path', coalesce(file_path, ''))`

func (s *Server) faxJob(ctx context.Context, dom domainRow, id string) (map[string]string, error) {
	rows, err := s.store.JSONRows(ctx, `select `+faxJobJSON+` from fax_jobs where id = $1::uuid and domain_uuid = $2::uuid`, id, dom.uuid)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, notFound("fax")
	}
	return rows[0], nil
}

// publicJob is a job as the page shows it (no server paths).
func publicJob(j map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range j {
		if k != "file_path" {
			out[k] = v
		}
	}
	has := "false"
	if j["file_path"] != "" {
		if _, err := os.Stat(j["file_path"]); err == nil {
			has = "true"
		}
	}
	out["has_file"] = has
	return out
}

// faxTracePath is where the core keeps a fax call's audio ("" for no call).
func (s *Server) faxTracePath(dom domainRow, callUUID, side string) string {
	if !reUUID.MatchString(callUUID) {
		return ""
	}
	return filepath.Join(s.faxDir(), dom.uuid, "trace", callUUID+"-"+side+".wav")
}

func (s *Server) faxJobs(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow) error {
	q := r.URL.Query()
	where := []string{"domain_uuid = $1::uuid"}
	args := []any{dom.uuid}
	if d := q.Get("direction"); d == "in" || d == "out" {
		args = append(args, d)
		where = append(where, fmt.Sprintf("direction = $%d", len(args)))
	}
	switch st := q.Get("status"); st {
	case "":
	case "active":
		where = append(where, "status in ('queued', 'dialing', 'sending', 'receiving')")
	default:
		args = append(args, st)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if n := strings.TrimSpace(q.Get("q")); n != "" {
		args = append(args, "%"+strings.ToLower(n)+"%")
		where = append(where, fmt.Sprintf("(lower(coalesce(from_number, '') || ' ' || coalesce(to_number, '') || ' ' || coalesce(error_reason, '')) like $%d)", len(args)))
	}
	limit := atoi(q.Get("limit"), 200)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args = append(args, limit)
	rows, err := s.store.JSONRows(ctx, `select `+faxJobJSON+` from fax_jobs where `+strings.Join(where, " and ")+
		fmt.Sprintf(` order by created_at desc limit $%d`, len(args)), args...)
	if err != nil {
		return err
	}
	out := make([]map[string]string, 0, len(rows))
	for _, j := range rows {
		pj := publicJob(j)
		pj["has_trace"] = "false"
		if p := s.faxTracePath(dom, j["call_uuid"], "heard"); p != "" {
			if _, err := os.Stat(p); err == nil {
				pj["has_trace"] = "true"
			}
		}
		out = append(out, pj)
	}
	writeJSON(w, out)
	return nil
}

// faxNumbers are the numbers a fax can be sent from: the domain's DIDs
// (and RingQ's q_dids), with the trunk each goes out on.
func (s *Server) faxNumbers(ctx context.Context, dom domainRow) ([]map[string]string, error) {
	out := []map[string]string{}
	seen := map[string]bool{}
	if s.store.HasTable("v_destinations") {
		rows, err := s.list(ctx, resourceByName("dids"), dom)
		if err != nil {
			return nil, err
		}
		names := s.gatewayNames(ctx, dom)
		faxExts := map[string]bool{}
		if s.store.HasTable("v_fax") {
			exts, err := s.store.JSONRows(ctx, `select jsonb_build_object('ext', coalesce(fax_extension, '')) from v_fax where domain_uuid = $1::uuid`, dom.uuid)
			if err != nil {
				return nil, err
			}
			for _, e := range exts {
				faxExts[e["ext"]] = true
			}
		}
		for _, d := range rows {
			if !truthy(firstNonEmpty(d["destination_enabled"], "true")) || d["destination_number"] == "" {
				continue
			}
			trunk := "Outbound rules"
			if g := d["gateway_uuid"]; g != "" {
				trunk = firstNonEmpty(names[g], g)
			}
			n := d["destination_number"]
			seen[n] = true
			fx := faxExtOf(d["action"])
			if !faxExts[fx] {
				fx = ""
			}
			out = append(out, map[string]string{"number": n, "label": d["destination_description"], "trunk": trunk, "fax": fx})
		}
	}
	if s.store.HasTable("q_dids") {
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('number', did_number, 'label', coalesce(name, ''),
			'trunk', coalesce(sip_trunk_name, '')) from q_dids where domain_uuid = $1::uuid order by did_number`, dom.uuid)
		if err == nil {
			for _, d := range rows {
				if !seen[d["number"]] {
					seen[d["number"]] = true
					d["trunk"] = firstNonEmpty(d["trunk"], "Outbound rules")
					out = append(out, d)
				}
			}
		}
	}
	return out, nil
}

// faxExtOf returns the extension a DID's action transfers to.
func faxExtOf(action string) string {
	if rest, ok := strings.CutPrefix(action, "transfer:"); ok {
		return strings.Fields(rest + " ")[0]
	}
	return ""
}

func (s *Server) gatewayNames(ctx context.Context, dom domainRow) map[string]string {
	names := map[string]string{}
	if !s.store.HasTable("v_gateways") {
		return names
	}
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('uuid', gateway_uuid, 'name', coalesce(gateway, ''))
		from v_gateways where domain_uuid = $1::uuid or domain_uuid is null`, dom.uuid)
	if err != nil {
		return names
	}
	for _, g := range rows {
		names[g["uuid"]] = g["name"]
	}
	return names
}

// faxIdentity is the station id and header line a domain's faxes carry:
// its settings, else the company name (RingQ's q_companies), else "Fax".
func (s *Server) faxIdentity(ctx context.Context, domUUID string) (ident, header, company string) {
	if s.store.HasTable("q_companies") {
		_ = s.store.Pool().QueryRow(ctx, `select coalesce(company_name, '') from q_companies where domain_uuid = $1::uuid order by id limit 1`,
			domUUID).Scan(&company)
	}
	_ = s.store.Pool().QueryRow(ctx, `select coalesce(fax_ident, ''), coalesce(fax_header, '') from fax_domain_settings where domain_uuid = $1::uuid`,
		domUUID).Scan(&ident, &header)
	def := firstNonEmpty(company, "Fax")
	return firstNonEmpty(ident, def), firstNonEmpty(header, def), company
}

func (s *Server) faxOverview(ctx context.Context, w http.ResponseWriter, dom domainRow) error {
	counts := map[string]int{}
	rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('direction', direction, 'status', status, 'n', count(*), 'pages', coalesce(sum(pages), 0))
		from fax_jobs where domain_uuid = $1::uuid group by direction, status`, dom.uuid)
	if err != nil {
		return err
	}
	for _, r := range rows {
		n := atoi(r["n"], 0)
		switch {
		case r["status"] == "sent":
			counts["sent"] += n
			counts["pages_sent"] += atoi(r["pages"], 0)
		case r["status"] == "received":
			counts["received"] += n
			counts["pages_received"] += atoi(r["pages"], 0)
		case r["status"] == "failed":
			counts["failed"] += n
		case faxBusy(r["status"]):
			counts["active"] += n
		}
	}
	today, err := s.store.JSONRows(ctx, `select jsonb_build_object('status', status, 'n', count(*)) from fax_jobs
		where domain_uuid = $1::uuid and created_at >= $2 group by status`, dom.uuid, float64(startOfDay().Unix()))
	if err != nil {
		return err
	}
	for _, r := range today {
		counts["today_"+r["status"]] += atoi(r["n"], 0)
	}
	numbers, err := s.faxNumbers(ctx, dom)
	if err != nil {
		return err
	}
	ident, header, company := s.faxIdentity(ctx, dom.uuid)
	var set struct{ ident, header string }
	_ = s.store.Pool().QueryRow(ctx, `select coalesce(fax_ident, ''), coalesce(fax_header, '') from fax_domain_settings where domain_uuid = $1::uuid`,
		dom.uuid).Scan(&set.ident, &set.header)
	_, gsErr := exec.LookPath("gs")
	writeJSON(w, map[string]any{
		"counts": counts, "numbers": numbers, "ghostscript": gsErr == nil,
		"settings": map[string]string{"fax_ident": set.ident, "fax_header": set.header, "company_name": company,
			"ident": ident, "header": header},
		"has_fax_extensions": s.store.HasTable("v_fax"),
	})
	return nil
}

func startOfDay() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
}

// faxSend queues a document to one or more numbers.
func (s *Server) faxSend(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow) error {
	q := r.URL.Query()
	from := cleanFaxNumber(q.Get("from"))
	var tos []string
	for _, t := range strings.FieldsFunc(q.Get("to"), func(c rune) bool { return c == ',' || c == ';' || c == '\n' }) {
		if t = cleanFaxNumber(t); t != "" {
			tos = append(tos, t)
		}
	}
	if len(tos) == 0 {
		return badRequest("enter the fax number to send to")
	}
	if len(tos) > faxMaxTo {
		return badRequest("%d numbers at most at once", faxMaxTo)
	}
	for _, t := range tos {
		if !reFaxNumber.MatchString(t) {
			return badRequest("fax number %s: digits, optionally starting with +", t)
		}
	}
	numbers, err := s.faxNumbers(ctx, dom)
	if err != nil {
		return err
	}
	ok := false
	for _, n := range numbers {
		if n["number"] == from {
			ok = true
		}
	}
	if !ok {
		return badRequest("choose the number to send from (one of your DID numbers)")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, faxMaxUpload))
	if err != nil {
		return badRequest("the document is too large (20 MB at most)")
	}
	if len(data) == 0 {
		return badRequest("attach the document to fax (PDF, TIFF or an image)")
	}
	ext := faxExt(data)
	if ext == "" {
		return badRequest("the document must be a PDF, TIFF, PNG, JPEG or GIF")
	}
	dir := filepath.Join(s.faxDir(), dom.uuid, "outbox")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Check it can be faxed now, not after dialling.
	probe := filepath.Join(dir, newUUID()+ext)
	if err := os.WriteFile(probe, data, 0o640); err != nil {
		return err
	}
	pages, err := fax.LoadDocument(probe)
	if err != nil {
		os.Remove(probe)
		if errors.Is(err, fax.ErrNoGhostscript) {
			return badRequest("sending a PDF needs the PDF converter on the server (sudo apt install ghostscript), or send a TIFF or image")
		}
		return badRequest("cannot read the document: %v", err)
	}
	var ids []string
	for i, to := range tos {
		id := newUUID()
		path := filepath.Join(dir, id+ext)
		if i == 0 {
			err = os.Rename(probe, path)
		} else {
			err = os.WriteFile(path, data, 0o640)
		}
		if err != nil {
			return err
		}
		now := epoch()
		if _, err := s.store.Pool().Exec(ctx, `insert into fax_jobs (id, domain_uuid, direction, from_number, to_number, pages, status,
			file_path, created_at, updated_at) values ($1::uuid, $2::uuid, 'out', $3, $4, 0, 'queued', $5, $6, $6)`,
			id, dom.uuid, from, to, path, now); err != nil {
			os.Remove(path)
			return err
		}
		ids = append(ids, id)
	}
	name := filepath.Base(strings.TrimSpace(q.Get("name")))
	s.log.Info("efax: queued", "to", strings.Join(tos, ","), "from", from, "pages", len(pages), "file", name, "domain", dom.name, "by", p.key())
	s.sw.RecordEvent("fax", fmt.Sprintf("%s: fax of %d page(s) queued from %s to %s", dom.name, len(pages), from, strings.Join(tos, ", ")))
	writeJSON(w, map[string]any{"jobs": ids, "pages": len(pages)})
	return nil
}

func cleanFaxNumber(n string) string {
	return strings.Map(func(c rune) rune {
		if c == ' ' || c == '-' || c == '(' || c == ')' || c == '.' {
			return -1
		}
		return c
	}, strings.TrimSpace(n))
}

func faxExt(data []byte) string {
	switch {
	case strings.HasPrefix(string(data), "%PDF"):
		return ".pdf"
	case strings.HasPrefix(string(data), "II*\x00") || strings.HasPrefix(string(data), "MM\x00*"):
		return ".tif"
	}
	switch http.DetectContentType(data) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	}
	return ""
}

func (s *Server) faxFile(w http.ResponseWriter, r *http.Request, job map[string]string) error {
	path := job["file_path"]
	f, err := os.Open(path)
	if err != nil {
		return notFound("file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return notFound("file")
	}
	head := make([]byte, 512)
	n, _ := f.Read(head)
	_, _ = f.Seek(0, io.SeekStart)
	ctype, ext := http.DetectContentType(head[:n]), filepath.Ext(path)
	if ext == ".tif" {
		ctype = "image/tiff"
	}
	name := "fax-" + job["direction"] + "-" + strings.TrimPrefix(firstNonEmpty(job["to_number"], job["from_number"]), "+") +
		"-" + time.Unix(int64(atof(job["created_at"])), 0).Format("20060102-1504") + ext
	disp := "inline"
	if r.URL.Query().Get("download") != "" {
		disp = "attachment"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disp, name))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, st.ModTime(), f)
	return nil
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// ---- the worker

// faxLoop sends the queued faxes and files the results of fax calls.
func (s *Server) faxLoop(stop <-chan struct{}) {
	if s.store == nil {
		return
	}
	sub := s.sw.Events().Subscribe(1024, isFaxEvent)
	defer sub.Close()
	s.faxRecover()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case e := <-sub.C:
			s.faxEvent(e)
		case <-t.C:
			s.faxDispatch()
		}
	}
}

func isFaxEvent(e *core.Event) bool {
	if e.Name == core.EvCustom {
		return strings.HasPrefix(e.Subclass(), "spandsp::")
	}
	if e.Name == core.EvChannelAnswer || e.Name == core.EvChannelHangupComplete {
		d := e.Get("variable_fax_direction")
		return d == "in" || d == "out"
	}
	return false
}

// faxRecover fails the faxes a restart cut short.
func (s *Server) faxRecover() {
	if s.faxReady() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, err := s.store.Pool().Exec(ctx, `update fax_jobs set status = 'failed', error_reason = 'RingQswitch restarted during the fax',
		updated_at = $1 where status in ('dialing', 'sending', 'receiving')`, epoch())
	if err != nil {
		s.log.Warn("efax: recovering jobs", "err", err)
	} else if tag.RowsAffected() > 0 {
		s.log.Warn("efax: faxes cut short by a restart marked failed", "jobs", tag.RowsAffected())
	}
}

func (s *Server) faxDispatch() {
	if s.faxReady() != nil {
		return
	}
	s.mu.Lock()
	free := faxMaxCalls - len(s.faxActive)
	s.mu.Unlock()
	if free <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jobs, err := s.store.JSONRows(ctx, `update fax_jobs j set status = 'dialing', updated_at = $1 from v_domains d
		where j.id in (select id from fax_jobs where direction = 'out' and status = 'queued' order by created_at limit $2 for update skip locked)
		and d.domain_uuid = j.domain_uuid
		returning jsonb_build_object('id', j.id, 'domain_uuid', j.domain_uuid, 'domain', d.domain_name, 'from', coalesce(j.from_number, ''),
			'to', coalesce(j.to_number, ''), 'file', coalesce(j.file_path, ''))`, epoch(), free)
	if err != nil {
		s.log.Warn("efax: queue", "err", err)
		return
	}
	for _, j := range jobs {
		s.mu.Lock()
		s.faxActive[j["id"]] = true
		s.mu.Unlock()
		go s.sendFax(j)
	}
}

// faxVar quotes a value for an originate {var=value} list.
func faxVar(v string) string {
	v = strings.Map(func(c rune) rune {
		if strings.ContainsRune("'{}[],\"\\", c) || c < ' ' {
			return ' '
		}
		return c
	}, v)
	return "'" + strings.TrimSpace(v) + "'"
}

func (s *Server) sendFax(j map[string]string) {
	defer func() {
		s.mu.Lock()
		delete(s.faxActive, j["id"])
		s.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ident, header, _ := s.faxIdentity(ctx, j["domain_uuid"])
	dial := s.faxRoute(ctx, j["domain_uuid"], j["domain"], j["from"], j["to"])
	cancel()
	if _, err := os.Stat(j["file"]); err != nil {
		s.faxFinal(j["id"], "failed", -1, "The document to send is missing")
		return
	}
	vars := []string{
		"origination_caller_id_number=" + strings.TrimPrefix(j["from"], "+"),
		"origination_caller_id_name=" + faxVar(header),
		"fax_job_id=" + j["id"],
		"fax_direction=out",
		"fax_ident=" + faxVar(ident),
		"fax_header=" + faxVar(header),
		"domain_uuid=" + j["domain_uuid"],
		"domain_name=" + j["domain"],
		"ignore_early_media=true",
		"call_timeout=" + strconv.Itoa(faxRingSeconds),
	}
	cmd := fmt.Sprintf("originate {%s}%s &txfax(%s)", strings.Join(vars, ","), dial, j["file"])
	s.log.Info("efax: dialling", "job", j["id"], "to", j["to"], "from", j["from"], "via", dial, "domain", j["domain"])
	out := strings.TrimSpace(s.sw.API(cmd))
	if uuid, ok := strings.CutPrefix(out, "+OK "); ok {
		uctx, ucancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ucancel()
		if _, err := s.store.Pool().Exec(uctx, `update fax_jobs set status = 'sending', call_uuid = $2, updated_at = $3
			where id = $1::uuid and status = 'dialing'`, j["id"], strings.TrimSpace(uuid), epoch()); err != nil {
			s.log.Warn("efax: job update", "err", err)
		}
		return
	}
	cause := strings.TrimSpace(strings.TrimPrefix(out, "-ERR"))
	s.faxFinal(j["id"], "failed", -1, faxCauseText(cause))
	s.log.Info("efax: not sent", "job", j["id"], "to", j["to"], "cause", cause)
}

// faxRoute picks how a fax from a number goes out: the trunk of that DID,
// RingQ's trunk for it (q_dids), else the domain's outbound rules.
func (s *Server) faxRoute(ctx context.Context, domUUID, domain, from, to string) string {
	to = strings.TrimSpace(to)
	if s.store.HasTable("v_destinations") {
		var conds string
		err := s.store.Pool().QueryRow(ctx, `select coalesce(destination_conditions::text, '') from v_destinations where domain_uuid = $1::uuid
			and destination_number in ($2, ltrim($2, '+')) order by destination_number limit 1`, domUUID, from).Scan(&conds)
		if err == nil {
			var cs []map[string]string
			if json.Unmarshal([]byte(conds), &cs) == nil {
				for _, c := range cs {
					if c["condition_field"] == "${gateway_uuid}" {
						if gw := strings.Trim(c["condition_expression"], "^$"); reUUID.MatchString(gw) && s.gatewayEnabled(ctx, gw) {
							return "ringq/gateway/" + gw + "/" + to
						}
					}
				}
			}
		}
	}
	if s.store.HasTable("q_dids") && s.store.HasTable("v_gateways") {
		var gw string
		err := s.store.Pool().QueryRow(ctx, `select g.gateway_uuid::text from q_dids d join v_gateways g on g.gateway = d.sip_trunk_name
			and g.domain_uuid = d.domain_uuid where d.domain_uuid = $1::uuid and d.did_number in ($2, ltrim($2, '+'))
			and coalesce(g.enabled::text, 'true') = 'true' limit 1`, domUUID, from).Scan(&gw)
		if err == nil && gw != "" {
			return "ringq/gateway/" + gw + "/" + to
		}
	}
	return "loopback/" + to + "/" + domain
}

func (s *Server) gatewayEnabled(ctx context.Context, gw string) bool {
	if !s.store.HasTable("v_gateways") {
		return false
	}
	var on string
	if err := s.store.Pool().QueryRow(ctx, `select coalesce(enabled::text, 'true') from v_gateways where gateway_uuid = $1::uuid`, gw).Scan(&on); err != nil {
		return false
	}
	return truthy(on)
}

var faxCauses = map[string]string{
	"USER_BUSY":                 "Busy",
	"NO_ANSWER":                 "No answer",
	"NO_USER_RESPONSE":          "No answer",
	"CALL_REJECTED":             "The call was rejected",
	"UNALLOCATED_NUMBER":        "The number is not in service",
	"NO_ROUTE_DESTINATION":      "No route to this number: check the trunk or the outbound rules",
	"INVALID_NUMBER_FORMAT":     "The number is not valid",
	"NORMAL_TEMPORARY_FAILURE":  "The carrier could not connect the call",
	"RECOVERY_ON_TIMER_EXPIRE":  "The carrier did not answer",
	"GATEWAY_DOWN":              "The trunk is down",
	"DESTINATION_OUT_OF_ORDER":  "The number is out of order",
	"ORIGINATOR_CANCEL":         "Cancelled",
	"INCOMPATIBLE_DESTINATION":  "The far end does not accept G.711 audio",
	"SUBSCRIBER_ABSENT":         "The number is not reachable",
	"EXCHANGE_ROUTING_ERROR":    "No route to this number",
	"SERVICE_UNAVAILABLE":       "The carrier is unavailable",
	"NETWORK_OUT_OF_ORDER":      "The network is out of order",
	"NORMAL_CIRCUIT_CONGESTION": "The carrier is busy (congestion)",
}

func faxCauseText(cause string) string {
	if t, ok := faxCauses[cause]; ok {
		return t + " (" + cause + ")"
	}
	return firstNonEmpty(cause, "The call failed")
}

// faxFinal sets a job's final status (pages < 0 keeps the count).
func (s *Server) faxFinal(id, status string, pages int, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	if pages >= 0 {
		_, err = s.store.Pool().Exec(ctx, `update fax_jobs set status = $2, pages = $3, error_reason = $4, updated_at = $5
			where id = $1::uuid and status in ('queued', 'dialing', 'sending', 'receiving')`, id, status, pages, nullIfEmpty(reason), epoch())
	} else {
		_, err = s.store.Pool().Exec(ctx, `update fax_jobs set status = $2, error_reason = $3, updated_at = $4
			where id = $1::uuid and status in ('queued', 'dialing', 'sending', 'receiving')`, id, status, nullIfEmpty(reason), epoch())
	}
	if err != nil {
		s.log.Warn("efax: job update", "job", id, "err", err)
	}
}

// faxEvent files what fax calls report.
func (s *Server) faxEvent(e *core.Event) {
	if s.faxReady() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := e.Get("Unique-ID")
	jobOf := func() string { // the job of an event's call
		if id := e.Get("variable_fax_job_id"); reUUID.MatchString(id) {
			return id
		}
		var id string
		if call != "" {
			_ = s.store.Pool().QueryRow(ctx, `select id::text from fax_jobs where call_uuid = $1 order by created_at desc limit 1`, call).Scan(&id)
		}
		return id
	}
	pool := s.store.Pool()
	switch {
	case e.Name == core.EvChannelAnswer && e.Get("variable_fax_direction") == "in":
		dom := e.Get("variable_domain_uuid")
		if !reUUID.MatchString(dom) {
			s.log.Warn("efax: a fax call without its domain", "uuid", call)
			return
		}
		to := firstNonEmpty(e.Get("variable_caller_destination"), e.Get("variable_sip_to_user"), e.Get("Caller-Destination-Number"))
		now := epoch()
		if _, err := pool.Exec(ctx, `insert into fax_jobs (id, domain_uuid, direction, from_number, to_number, pages, status, file_path,
			call_uuid, created_at, updated_at) values ($1::uuid, $2::uuid, 'in', $3, $4, 0, 'receiving', $5, $6, $7, $7)`,
			newUUID(), dom, e.Get("Caller-Caller-ID-Number"), to, e.Get("variable_fax_file"), call, now); err != nil {
			s.log.Warn("efax: inbound job", "err", err)
		}
	case e.Subclass() == "spandsp::txfaxpageresult" || e.Subclass() == "spandsp::rxfaxpageresult":
		if id := jobOf(); id != "" {
			_, _ = pool.Exec(ctx, `update fax_jobs set pages = $2, updated_at = $3 where id = $1::uuid`, id,
				atoi(e.Get("fax-document-transferred-pages"), 0), epoch())
		}
	case e.Subclass() == "spandsp::txfaxresult" || e.Subclass() == "spandsp::rxfaxresult":
		id := jobOf()
		if id == "" {
			return
		}
		ok := e.Get("fax-success") == "1"
		pages := atoi(e.Get("fax-document-transferred-pages"), 0)
		status, reason := "sent", ""
		if e.Subclass() == "spandsp::rxfaxresult" {
			status = "received"
		}
		if !ok {
			status, reason = "failed", e.Get("fax-result-text")
		}
		s.faxFinal(id, status, pages, reason)
		if e.Subclass() == "spandsp::rxfaxresult" && pages > 0 {
			if f := e.Get("fax-filename"); f != "" {
				_, _ = pool.Exec(ctx, `update fax_jobs set file_path = $2 where id = $1::uuid`, id, f)
			}
		}
		what := "sent to " + e.Get("Caller-Destination-Number")
		if status == "received" {
			what = "received from " + e.Get("Caller-Caller-ID-Number")
		}
		s.log.Info("efax: "+status, "job", id, "pages", pages, "remote", e.Get("fax-remote-station-id"), "result", e.Get("fax-result-text"))
		s.sw.RecordEvent("fax", fmt.Sprintf("fax %s: %s, %d page(s), %s", what, status, pages, e.Get("fax-result-text")))
	case e.Name == core.EvChannelHangupComplete:
		// A call that ended with its fax unfinished (and no result). One
		// that never connected is the sender's to report (still dialing).
		if id := jobOf(); id != "" {
			cause := firstNonEmpty(e.Get("Hangup-Cause"), "hangup")
			if _, err := pool.Exec(ctx, `update fax_jobs set status = 'failed', error_reason = $2, updated_at = $3
				where id = $1::uuid and status in ('sending', 'receiving')`, id, "The call ended before the fax finished ("+cause+")", epoch()); err != nil {
				s.log.Warn("efax: job update", "job", id, "err", err)
			}
		}
	}
}
