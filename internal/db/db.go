// Package db connects RingQswitch to a PostgreSQL database that uses the
// RingQ schema (as the RingQ platform does). It replaces what
// the previous switch got from the RingQ portal's Lua XML handlers and its core database:
//
//   - load: domains, extensions, gateways, dialplans, global variables,
//     ring groups, IVR menus and carrier ACLs are read into the
//     configuration (Overlay), and reloaded when they change (Watch);
//   - write: registrations, channels and calls tables are kept up to date
//     and CDRs are inserted into v_xml_cdr (and any other table with the
//     same columns, such as q_ringq_cdr).
//
// Only the tables and columns that exist are used, so RingQ versions
// with extra or missing columns work.
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
)

// Store is a connection pool plus schema knowledge.
type Store struct {
	pool *pgxpool.Pool
	cfg  config.Database
	log  *slog.Logger

	mu      sync.Mutex
	columns map[string]map[string]string // table -> column -> data type
}

// Open connects to the database and reads the schema.
func Open(ctx context.Context, cfg config.Database, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("database dsn: %w", err)
	}
	if pc.MaxConns < 4 {
		pc.MaxConns = 4
	}
	pc.ConnConfig.RuntimeParams["application_name"] = "ringqswitch"
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(cctx, pc)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	if err := pool.Ping(cctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %w", err)
	}
	s := &Store{pool: pool, cfg: cfg, log: log}
	if err := s.loadSchema(cctx); err != nil {
		pool.Close()
		return nil, err
	}
	if cfg.ManageSchema {
		if err := s.EnsureSchema(cctx); err != nil {
			log.Warn("database: RingQswitch tables unavailable (AUX codes, agent status log, queue reports, outbound routes and time conditions need them)", "err", err)
		}
		if roles, err := s.ShareTables(cctx, cfg.GrantTo); err != nil {
			log.Warn("database: could not share RingQswitch's tables with the other database users", "err", err)
		} else if len(roles) > 0 {
			log.Info("database: RingQswitch's tables are readable and writable by", "roles", strings.Join(roles, ","))
		}
	}
	return s, nil
}

// Close closes the pool.
func (s *Store) Close() { s.pool.Close() }

// Pool exposes the connection pool (tests and tools).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) loadSchema(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `select table_name, column_name, data_type
		from information_schema.columns where table_schema = current_schema()`)
	if err != nil {
		return fmt.Errorf("database: reading schema: %w", err)
	}
	cols := map[string]map[string]string{}
	for rows.Next() {
		var t, c, dt string
		if err := rows.Scan(&t, &c, &dt); err != nil {
			rows.Close()
			return err
		}
		if cols[t] == nil {
			cols[t] = map[string]string{}
		}
		cols[t][c] = dt
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.columns = cols
	s.mu.Unlock()
	return nil
}

// HasTable reports whether a table exists.
func (s *Store) HasTable(t string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.columns[t] != nil
}

// HasColumn reports whether a table has a column.
func (s *Store) HasColumn(t, c string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.columns[t][c]
	return ok
}

// ColumnType returns a column's SQL data type ("" if it does not exist).
func (s *Store) ColumnType(t, c string) string { return s.columnType(t, c) }

// Columns returns the column names of a table.
func (s *Store) Columns(t string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.columns[t]))
	for c := range s.columns[t] {
		out = append(out, c)
	}
	return out
}

// JSONRows runs a query returning one to_jsonb object per row and returns
// the rows with values rendered as strings.
func (s *Store) JSONRows(ctx context.Context, sql string, args ...any) ([]map[string]string, error) {
	return s.jsonRows(ctx, sql, args...)
}

func (s *Store) columnType(t, c string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.columns[t][c]
}

// rowsAsMaps runs a query whose single column is a JSON object per row
// (to_jsonb) and returns the objects with values rendered as strings.
func (s *Store) jsonRows(ctx context.Context, sql string, args ...any) ([]map[string]string, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var m map[string]any
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, stringify(m))
	}
	return out, rows.Err()
}

func stringify(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch x := v.(type) {
		case nil:
		case string:
			out[k] = x
		case bool:
			if x {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
		case float64:
			if x == float64(int64(x)) {
				out[k] = fmt.Sprintf("%d", int64(x))
			} else {
				out[k] = fmt.Sprintf("%g", x)
			}
		case map[string]any, []any:
			b, _ := json.Marshal(x) // json/jsonb columns
			out[k] = string(b)
		default:
			out[k] = fmt.Sprint(x)
		}
	}
	return out
}

// truthy accepts the boolean spellings found in RingQ tables (text
// columns hold 'true'/'false', newer ones are boolean).
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "t", "1", "yes", "on", "enabled":
		return true
	}
	return false
}

// enabled treats a missing value as enabled.
func enabled(v string) bool { return strings.TrimSpace(v) == "" || truthy(v) }

func quoteIdent(name string) string { return pgx.Identifier{name}.Sanitize() }
