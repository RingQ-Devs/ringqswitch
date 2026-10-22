package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ShareTables lets other database users read and change the tables
// RingQswitch's own user owns (the rq_* tables it creates, and any
// RingQ table it had to create), so the RingQ portal or a database tool signed
// in as another user does not get "permission denied". The roles are the
// database's owner, the owner of v_domains, and extra. Tables RingQswitch
// creates later are covered too (default privileges). It returns the roles
// that were granted access.
func (s *Store) ShareTables(ctx context.Context, extra []string) ([]string, error) {
	var me, dbOwner, portalOwner string
	if err := s.pool.QueryRow(ctx, `select current_user, coalesce((select pg_get_userbyid(datdba) from pg_database where datname = current_database()), ''),
		coalesce((select tableowner from pg_tables where schemaname = current_schema() and tablename = 'v_domains'), '')`).Scan(&me, &dbOwner, &portalOwner); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	var roles []string
	for _, r := range append([]string{dbOwner, portalOwner}, extra...) {
		if r == "" || r == me || want[r] {
			continue
		}
		var ok bool
		if err := s.pool.QueryRow(ctx, `select exists (select 1 from pg_roles where rolname = $1)`, r).Scan(&ok); err != nil {
			return nil, err
		}
		if !ok {
			s.log.Warn("database grant-to: no such database user", "role", r)
			continue
		}
		want[r] = true
		roles = append(roles, r)
	}
	if len(roles) == 0 {
		return nil, nil
	}
	var tables, seqs []string
	rows, err := s.pool.Query(ctx, `select c.relname, c.relkind::text from pg_class c join pg_namespace n on n.oid = c.relnamespace
		where n.nspname = current_schema() and c.relkind in ('r', 'p', 'S') and pg_get_userbyid(c.relowner) = current_user`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return nil, err
		}
		if kind == "S" {
			seqs = append(seqs, name)
		} else {
			tables = append(tables, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range roles {
		role := pgx.Identifier{r}.Sanitize()
		for _, t := range tables {
			if _, err := s.pool.Exec(ctx, fmt.Sprintf(`grant select, insert, update, delete on %s to %s`, pgx.Identifier{t}.Sanitize(), role)); err != nil {
				return roles, fmt.Errorf("grant on %s to %s: %w", t, r, err)
			}
		}
		for _, q := range seqs {
			if _, err := s.pool.Exec(ctx, fmt.Sprintf(`grant usage, select on sequence %s to %s`, pgx.Identifier{q}.Sanitize(), role)); err != nil {
				return roles, fmt.Errorf("grant on %s to %s: %w", q, r, err)
			}
		}
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`alter default privileges in schema %s grant select, insert, update, delete on tables to %s`,
			pgx.Identifier{s.schemaName(ctx)}.Sanitize(), role)); err != nil {
			return roles, fmt.Errorf("default privileges for %s: %w", r, err)
		}
	}
	return roles, nil
}

func (s *Store) schemaName(ctx context.Context) string {
	var n string
	if err := s.pool.QueryRow(ctx, `select current_schema()`).Scan(&n); err != nil || n == "" {
		return "public"
	}
	return n
}
