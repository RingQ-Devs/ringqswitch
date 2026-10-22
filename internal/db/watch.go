package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Tables whose changes trigger a configuration reload.
var watchTables = []string{
	"v_domains", "v_extensions", "v_gateways", "v_dialplans", "v_vars",
	"v_ring_groups", "v_ring_group_destinations", "v_ivr_menus", "v_ivr_menu_options",
	"v_access_controls", "v_access_control_nodes",
	"v_call_center_queues", "v_call_center_agents", "v_call_center_tiers", "rq_aux_codes",
	"v_voicemails", "v_voicemail_messages", "v_conferences", "rq_security_rules",
}

// Fingerprint summarises the content of the configuration tables; it
// changes whenever a row is added, removed or modified, whether or not the
// application maintained update_date.
func (s *Store) Fingerprint(ctx context.Context) (string, error) {
	var parts []string
	for _, t := range watchTables {
		if !s.HasTable(t) {
			continue
		}
		q := fmt.Sprintf(`select count(*)::text || ':' || coalesce(sum(hashtext(x::text)::bigint), 0)::text from %s x`, quoteIdent(t))
		var v string
		if err := s.pool.QueryRow(ctx, q).Scan(&v); err != nil {
			return "", fmt.Errorf("%s: %w", t, err)
		}
		parts = append(parts, t+"="+v)
	}
	return strings.Join(parts, ","), nil
}

// Watch polls the fingerprint every interval and calls reload when it
// changes, until ctx is cancelled.
func (s *Store) Watch(ctx context.Context, interval time.Duration, reload func() error) {
	if interval <= 0 {
		return
	}
	last, err := s.Fingerprint(ctx)
	if err != nil {
		s.log.Warn("database change detection unavailable", "err", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		fp, err := s.Fingerprint(qctx)
		cancel()
		if err != nil {
			s.log.Warn("database change detection failed", "err", err)
			continue
		}
		if fp == last {
			continue
		}
		s.log.Info("database configuration changed; reloading")
		if err := reload(); err != nil {
			s.log.Error("reload after database change failed (keeping the previous configuration)", "err", err)
			continue // retry on the next tick
		}
		last = fp
	}
}
