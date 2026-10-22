package web

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Hotel front desk: the domain's extensions are the rooms. A guest checks
// in to a room (rq_hotel_stays) and out again; a checked-in guest can have
// a wake-up call (rq_wakeup_calls): at its time RingQswitch rings the room
// and plays the wake-up sound, and tries again when nobody answers.
//
//	GET    /api/d/{domain}/hotel                     rooms, guests, wake-up calls
//	POST   /api/d/{domain}/hotel/checkin             {extension, first_name, last_name}
//	PUT    /api/d/{domain}/hotel/stays/{uuid}        {first_name, last_name}
//	POST   /api/d/{domain}/hotel/stays/{uuid}/checkout
//	POST   /api/d/{domain}/hotel/wakeups             {extension, at: "2026-10-10T07:00", sound}
//	DELETE /api/d/{domain}/hotel/wakeups/{uuid}

const (
	wakeupTries    = 3               // rings of the room before giving up
	wakeupRetry    = 5 * time.Minute // between them
	wakeupRingTime = 45              // seconds a wake-up call rings
	// The default wake-up sound: a minute of beeps.
	wakeupTone = "tone_stream://%(800,400,880);%(800,1200,1175);loops=25"
)

var reGuestName = regexp.MustCompile(`^[\p{L}\p{M}0-9 .,'’-]{0,60}$`)

func (s *Server) hotelAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if !s.store.HasTable("rq_hotel_stays") || !s.store.HasTable("rq_wakeup_calls") {
		return badRequest("the hotel tables are missing: restart RingQswitch once to add them")
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		return s.hotelRooms(ctx, w, dom)
	case part(parts, 0) == "checkin" && r.Method == http.MethodPost:
		var b struct{ Extension, FirstName, LastName string }
		if err := decodeHotel(r, &b); err != nil {
			return err
		}
		if err := s.checkRoom(ctx, dom, b.Extension); err != nil {
			return err
		}
		var open int
		if err := s.store.Pool().QueryRow(ctx, `select count(*) from rq_hotel_stays where domain_uuid = $1::uuid and extension = $2
			and checked_out_at is null`, dom.uuid, b.Extension).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return badRequest("room %s is occupied: check the guest out first", b.Extension)
		}
		id := newUUID()
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_hotel_stays (stay_uuid, domain_uuid, extension, first_name, last_name, checked_in_by)
			values ($1, $2, $3, $4, $5, $6)`, id, dom.uuid, b.Extension, b.FirstName, b.LastName, p.key()); err != nil {
			return err
		}
		s.log.Info("hotel: check-in", "room", b.Extension, "domain", dom.name, "by", p.key())
		s.sw.RecordEvent("hotel", fmt.Sprintf("%s: %s %s checked in to room %s", dom.name, b.FirstName, b.LastName, b.Extension))
		writeJSON(w, map[string]string{"stay_uuid": id})
		return nil
	case part(parts, 0) == "stays" && reUUID.MatchString(part(parts, 1)) && len(parts) == 2 && r.Method == http.MethodPut:
		var b struct{ Extension, FirstName, LastName string }
		if err := decodeHotel(r, &b); err != nil {
			return err
		}
		tag, err := s.store.Pool().Exec(ctx, `update rq_hotel_stays set first_name = $3, last_name = $4 where stay_uuid = $1::uuid
			and domain_uuid = $2::uuid and checked_out_at is null`, parts[1], dom.uuid, b.FirstName, b.LastName)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("guest")
		}
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil
	case part(parts, 0) == "stays" && reUUID.MatchString(part(parts, 1)) && part(parts, 2) == "checkout" && r.Method == http.MethodPost:
		var ext string
		err := s.store.Pool().QueryRow(ctx, `update rq_hotel_stays set checked_out_at = now(), checked_out_by = $3
			where stay_uuid = $1::uuid and domain_uuid = $2::uuid and checked_out_at is null returning extension`, parts[1], dom.uuid, p.key()).Scan(&ext)
		if err == pgx.ErrNoRows {
			return notFound("guest")
		}
		if err != nil {
			return err
		}
		// The guest's wake-up calls go with the guest.
		_, _ = s.store.Pool().Exec(ctx, `update rq_wakeup_calls set status = 'cancelled', last_result = 'checked out', update_date = now()
			where stay_uuid = $1::uuid and status = 'scheduled'`, parts[1])
		s.log.Info("hotel: check-out", "room", ext, "domain", dom.name, "by", p.key())
		s.sw.RecordEvent("hotel", fmt.Sprintf("%s: room %s checked out", dom.name, ext))
		writeJSON(w, map[string]string{"ok": "checked out"})
		return nil
	case part(parts, 0) == "wakeups" && len(parts) == 1 && r.Method == http.MethodPost:
		var b struct{ Extension, At, Sound string }
		if err := decodeBody(r, &b); err != nil {
			return err
		}
		b.Extension = strings.TrimSpace(b.Extension)
		var stay string
		if err := s.store.Pool().QueryRow(ctx, `select stay_uuid::text from rq_hotel_stays where domain_uuid = $1::uuid and extension = $2
			and checked_out_at is null`, dom.uuid, b.Extension).Scan(&stay); err != nil {
			return badRequest("room %s has no guest: check one in first", b.Extension)
		}
		at, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(b.At), time.Local)
		if err != nil {
			if at, err = time.Parse(time.RFC3339, strings.TrimSpace(b.At)); err != nil {
				return badRequest("wake-up time: choose a date and time")
			}
		}
		if at.Before(time.Now().Add(-time.Minute)) {
			return badRequest("the wake-up time has passed")
		}
		if b.Sound != "" && !strings.HasPrefix(b.Sound, "prompts/") {
			return badRequest("sound: one of the prompts (Sounds & music)")
		}
		// One wake-up call per room: a new one replaces the one planned.
		if _, err := s.store.Pool().Exec(ctx, `update rq_wakeup_calls set status = 'cancelled', last_result = 'replaced', update_date = now()
			where domain_uuid = $1::uuid and extension = $2 and status = 'scheduled'`, dom.uuid, b.Extension); err != nil {
			return err
		}
		id := newUUID()
		if _, err := s.store.Pool().Exec(ctx, `insert into rq_wakeup_calls (wakeup_uuid, domain_uuid, extension, stay_uuid, call_at, sound, created_by)
			values ($1, $2, $3, $4::uuid, $5, $6, $7)`, id, dom.uuid, b.Extension, stay, at, b.Sound, p.key()); err != nil {
			return err
		}
		s.log.Info("hotel: wake-up call scheduled", "room", b.Extension, "at", at.Format("2006-01-02 15:04"), "by", p.key())
		writeJSON(w, map[string]string{"wakeup_uuid": id})
		return nil
	case part(parts, 0) == "wakeups" && reUUID.MatchString(part(parts, 1)) && r.Method == http.MethodDelete:
		tag, err := s.store.Pool().Exec(ctx, `update rq_wakeup_calls set status = 'cancelled', last_result = 'cancelled at the front desk',
			update_date = now() where wakeup_uuid = $1::uuid and domain_uuid = $2::uuid and status = 'scheduled'`, parts[1], dom.uuid)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("wake-up call")
		}
		writeJSON(w, map[string]string{"ok": "cancelled"})
		return nil
	}
	return errMethod
}

// decodeHotel reads a guest's details.
func decodeHotel(r *http.Request, b *struct{ Extension, FirstName, LastName string }) error {
	var raw struct {
		Extension string `json:"extension"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := decodeBody(r, &raw); err != nil {
		return err
	}
	b.Extension = strings.TrimSpace(raw.Extension)
	b.FirstName, b.LastName = strings.TrimSpace(raw.FirstName), strings.TrimSpace(raw.LastName)
	if b.FirstName == "" && b.LastName == "" {
		return badRequest("the guest's name: first or last name")
	}
	if !reGuestName.MatchString(b.FirstName) || !reGuestName.MatchString(b.LastName) {
		return badRequest("the guest's name: letters, spaces and . , ' - (up to 60)")
	}
	return nil
}

func (s *Server) checkRoom(ctx context.Context, dom domainRow, ext string) error {
	if !reNumber.MatchString(ext) {
		return badRequest("room: an extension number")
	}
	var n int
	if err := s.store.Pool().QueryRow(ctx, `select count(*) from v_extensions where domain_uuid = $1::uuid and extension = $2`, dom.uuid, ext).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return notFound("room " + ext)
	}
	return nil
}

func (s *Server) hotelRooms(ctx context.Context, w http.ResponseWriter, dom domainRow) error {
	exts, err := s.store.JSONRows(ctx, `select jsonb_build_object('extension', extension, 'extension_uuid', extension_uuid,
		'name', coalesce(nullif(trim(coalesce(directory_first_name, '') || ' ' || coalesce(directory_last_name, '')), ''), effective_caller_id_name, ''),
		'enabled', coalesce(enabled::text, 'true')) from v_extensions where domain_uuid = $1::uuid order by extension`, dom.uuid)
	if err != nil {
		return err
	}
	stays, err := s.store.JSONRows(ctx, `select jsonb_build_object('stay_uuid', stay_uuid, 'extension', extension, 'first_name', coalesce(first_name, ''),
		'last_name', coalesce(last_name, ''), 'checked_in_at', checked_in_at) from rq_hotel_stays
		where domain_uuid = $1::uuid and checked_out_at is null`, dom.uuid)
	if err != nil {
		return err
	}
	// The planned wake-up call of each room, else its latest one (the last 18 hours).
	wakes, err := s.store.JSONRows(ctx, `select distinct on (extension) jsonb_build_object('wakeup_uuid', wakeup_uuid, 'extension', extension,
		'call_at', call_at, 'status', status, 'attempts', attempts, 'last_result', coalesce(last_result, ''), 'sound', coalesce(sound, ''))
		from rq_wakeup_calls where domain_uuid = $1::uuid and (status in ('scheduled', 'calling')
			or (update_date > now() - interval '18 hours' and coalesce(last_result, '') <> 'replaced'))
		order by extension, (status in ('scheduled', 'calling')) desc, update_date desc`, dom.uuid)
	if err != nil {
		return err
	}
	byExt := func(rows []map[string]string) map[string]map[string]string {
		m := map[string]map[string]string{}
		for _, r := range rows {
			m[r["extension"]] = r
		}
		return m
	}
	st, wk := byExt(stays), byExt(wakes)
	online := map[string]bool{}
	for _, b := range s.sw.Registrar().All() {
		if strings.EqualFold(b.Domain, dom.name) {
			online[b.User] = true
		}
	}
	rooms := []map[string]any{}
	occupied := 0
	for _, e := range exts {
		if !truthy(firstNonEmpty(e["enabled"], "true")) {
			continue
		}
		room := map[string]any{"extension": e["extension"], "name": e["name"], "registered": online[e["extension"]], "status": "vacant"}
		if g := st[e["extension"]]; g != nil {
			room["status"], room["guest"] = "occupied", g
			occupied++
		}
		if x := wk[e["extension"]]; x != nil {
			room["wakeup"] = x
		}
		rooms = append(rooms, room)
	}
	writeJSON(w, map[string]any{"rooms": rooms, "occupied": occupied, "vacant": len(rooms) - occupied})
	return nil
}

// wakeupLoop places the wake-up calls that are due.
func (s *Server) wakeupLoop(stop <-chan struct{}) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.wakeupDue()
		}
	}
}

func (s *Server) wakeupDue() {
	if s.store == nil || !s.store.HasTable("rq_wakeup_calls") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A call left "calling" by a restart is tried again.
	if _, err := s.store.Pool().Exec(ctx, `update rq_wakeup_calls set status = 'scheduled', update_date = now()
		where status = 'calling' and update_date < now() - interval '3 minutes'`); err != nil {
		s.log.Warn("hotel: wake-up calls", "err", err)
	}
	rows, err := s.store.JSONRows(ctx, `update rq_wakeup_calls c set status = 'calling', attempts = attempts + 1, update_date = now()
		from v_domains d where d.domain_uuid = c.domain_uuid and c.status = 'scheduled' and c.call_at <= now()
		returning jsonb_build_object('wakeup_uuid', c.wakeup_uuid, 'extension', c.extension, 'domain', d.domain_name,
			'sound', coalesce(c.sound, ''), 'attempts', c.attempts)`)
	if err != nil {
		s.log.Warn("hotel: wake-up calls", "err", err)
		return
	}
	for _, c := range rows {
		go s.placeWakeup(c)
	}
}

// placeWakeup rings the room; answered, the guest hears the wake-up sound.
func (s *Server) placeWakeup(c map[string]string) {
	sound := firstNonEmpty(c["sound"], wakeupTone)
	cmd := fmt.Sprintf(`originate {origination_caller_id_name='Wake-up call',origination_caller_id_number=wakeup}user/%s@%s &playback(%s) XML default "Wake-up call" wakeup %d`,
		c["extension"], c["domain"], sound, wakeupRingTime)
	out := s.sw.API(cmd)
	answered := strings.HasPrefix(out, "+OK")
	status, result := "answered", "answered"
	var next any
	if !answered {
		result = strings.TrimSpace(strings.TrimPrefix(out, "-ERR"))
		status = "no_answer"
		if atoi(c["attempts"], wakeupTries) < wakeupTries {
			status, next = "scheduled", time.Now().Add(wakeupRetry)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.store.Pool().Exec(ctx, `update rq_wakeup_calls set status = $2, last_result = $3, call_at = coalesce($4, call_at), update_date = now()
		where wakeup_uuid = $1::uuid`, c["wakeup_uuid"], status, result, next); err != nil {
		s.log.Warn("hotel: wake-up call result", "err", err)
	}
	s.log.Info("hotel: wake-up call", "room", c["extension"], "domain", c["domain"], "try", c["attempts"], "result", result)
	s.sw.RecordEvent("hotel", fmt.Sprintf("%s: wake-up call to room %s (try %s): %s", c["domain"], c["extension"], c["attempts"], result))
}
