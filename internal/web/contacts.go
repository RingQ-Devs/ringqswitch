package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Contacts: the company's shared address book. Everybody in the domain can
// see and call contacts and add new ones; owners, managers and whoever
// added a contact can change or delete it; owners and managers can import
// and export them as CSV.
//
//	GET    /api/d/{domain}/contacts?q=&limit=&offset=   {rows, total}
//	POST   /api/d/{domain}/contacts                     {name, company, phone, mobile, email, notes}
//	PUT    /api/d/{domain}/contacts/{uuid}
//	DELETE /api/d/{domain}/contacts/{uuid}
//	GET    /api/d/{domain}/contacts/lookup?number=      {name, company} of the contact with that number
//	POST   /api/d/{domain}/contacts/import              CSV body (header: name,company,phone,mobile,email,notes)
//	GET    /api/d/{domain}/contacts/export              CSV file

var contactFields = []string{"name", "company", "phone", "mobile", "email", "notes"}

var (
	rePhone = regexp.MustCompile(`^\+?[0-9 ()./-]{2,40}$`)
	reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

type contact struct {
	Name, Company, Phone, Mobile, Email, Notes string
}

func (c *contact) clean() error {
	trim := func(s string, n int) string {
		s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
		if utf8.RuneCountInString(s) > n {
			s = string([]rune(s)[:n])
		}
		return s
	}
	c.Name, c.Company, c.Email = trim(c.Name, 100), trim(c.Company, 100), trim(c.Email, 120)
	c.Phone, c.Mobile, c.Notes = trim(c.Phone, 40), trim(c.Mobile, 40), trim(c.Notes, 1000)
	switch {
	case c.Name == "":
		return badRequest("name: required")
	case c.Phone == "" && c.Mobile == "":
		return badRequest("phone: enter a phone or mobile number")
	case c.Phone != "" && (!rePhone.MatchString(c.Phone) || digitsOf(c.Phone) == ""):
		return badRequest("phone: digits, spaces and + ( ) - only")
	case c.Mobile != "" && (!rePhone.MatchString(c.Mobile) || digitsOf(c.Mobile) == ""):
		return badRequest("mobile: digits, spaces and + ( ) - only")
	case c.Email != "" && !reEmail.MatchString(c.Email):
		return badRequest("email: not an email address")
	}
	return nil
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Server) contactsAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, p *principal, dom domainRow, parts []string) error {
	if !s.store.HasTable("rq_contacts") {
		return badRequest("the database has no rq_contacts table (restart RingQswitch to create it)")
	}
	id := part(parts, 0)
	pool := s.store.Pool()
	cols := `jsonb_build_object('contact_uuid', contact_uuid, 'name', name, 'company', coalesce(company, ''), 'phone', coalesce(phone, ''),
		'mobile', coalesce(mobile, ''), 'email', coalesce(email, ''), 'notes', coalesce(notes, ''), 'created_by', coalesce(created_by, ''),
		'update_date', update_date)`
	switch {
	case id == "" && r.Method == http.MethodGet:
		q := r.URL.Query()
		where, args := "domain_uuid = $1::uuid", []any{dom.uuid}
		if v := strings.TrimSpace(q.Get("q")); v != "" {
			args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v)+"%")
			n := len(args)
			cond := fmt.Sprintf("name ilike $%d or company ilike $%d or phone ilike $%d or mobile ilike $%d or email ilike $%d", n, n, n, n, n)
			if d := digitsOf(v); len(d) >= 3 {
				args = append(args, "%"+d+"%")
				cond += fmt.Sprintf(" or regexp_replace(coalesce(phone, ''), '[^0-9]', '', 'g') like $%d or regexp_replace(coalesce(mobile, ''), '[^0-9]', '', 'g') like $%d",
					len(args), len(args))
			}
			where += " and (" + cond + ")"
		}
		var total int
		if err := pool.QueryRow(ctx, "select count(*) from rq_contacts where "+where, args...).Scan(&total); err != nil {
			return err
		}
		limit := min(max(atoi(q.Get("limit"), 50), 1), 500)
		offset := max(atoi(q.Get("offset"), 0), 0)
		rows, err := s.store.JSONRows(ctx, fmt.Sprintf("select %s from rq_contacts where %s order by lower(name), contact_uuid limit %d offset %d",
			cols, where, limit, offset), args...)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]string{}
		}
		for _, c := range rows {
			c["can_edit"] = fmt.Sprint(p.supervisor() || c["created_by"] == p.key())
		}
		writeJSON(w, map[string]any{"rows": rows, "total": total, "can_import": p.supervisor()})
		return nil

	case id == "lookup" && r.Method == http.MethodGet:
		d := digitsOf(r.URL.Query().Get("number"))
		if len(d) < 3 {
			writeJSON(w, map[string]string{})
			return nil
		}
		if len(d) > 9 {
			d = d[len(d)-9:] // national/international forms of the same number
		}
		var name, company string
		err := pool.QueryRow(ctx, `select name, coalesce(company, '') from rq_contacts where domain_uuid = $1::uuid and
			(right(regexp_replace(coalesce(phone, ''), '[^0-9]', '', 'g'), $3) = $2 or right(regexp_replace(coalesce(mobile, ''), '[^0-9]', '', 'g'), $3) = $2)
			order by lower(name) limit 1`, dom.uuid, d, len(d)).Scan(&name, &company)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		writeJSON(w, map[string]string{"name": name, "company": company})
		return nil

	case id == "" && r.Method == http.MethodPost:
		var c contact
		if err := decodeBody(r, &c); err != nil {
			return err
		}
		if err := c.clean(); err != nil {
			return err
		}
		uuid := newUUID()
		if _, err := pool.Exec(ctx, `insert into rq_contacts (contact_uuid, domain_uuid, name, company, phone, mobile, email, notes, created_by, insert_date, update_date)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())`, uuid, dom.uuid, c.Name, c.Company, c.Phone, c.Mobile, c.Email, c.Notes, p.key()); err != nil {
			return err
		}
		writeJSON(w, map[string]string{"contact_uuid": uuid})
		return nil

	case reUUID.MatchString(id) && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		var owner string
		if err := pool.QueryRow(ctx, `select coalesce(created_by, '') from rq_contacts where contact_uuid = $1::uuid and domain_uuid = $2::uuid`,
			id, dom.uuid).Scan(&owner); err != nil {
			return notFound("contact")
		}
		if !p.supervisor() && owner != p.key() {
			return errForbidden
		}
		if r.Method == http.MethodDelete {
			_, err := pool.Exec(ctx, `delete from rq_contacts where contact_uuid = $1::uuid`, id)
			if err == nil {
				writeJSON(w, map[string]string{"ok": "deleted"})
			}
			return err
		}
		var c contact
		if err := decodeBody(r, &c); err != nil {
			return err
		}
		if err := c.clean(); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `update rq_contacts set name = $2, company = $3, phone = $4, mobile = $5, email = $6, notes = $7, update_date = now()
			where contact_uuid = $1::uuid`, id, c.Name, c.Company, c.Phone, c.Mobile, c.Email, c.Notes); err != nil {
			return err
		}
		writeJSON(w, map[string]string{"ok": "saved"})
		return nil

	case id == "import" && r.Method == http.MethodPost:
		if !p.supervisor() {
			return errForbidden
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
		if err != nil {
			return badRequest("the file is too large (5 MB at most)")
		}
		added, skipped, err := s.importContacts(ctx, dom, p, data)
		if err != nil {
			return err
		}
		writeJSON(w, map[string]any{"added": added, "skipped": skipped})
		return nil

	case id == "export" && r.Method == http.MethodGet:
		if !p.supervisor() {
			return errForbidden
		}
		rows, err := pool.Query(ctx, `select name, coalesce(company, ''), coalesce(phone, ''), coalesce(mobile, ''), coalesce(email, ''), coalesce(notes, '')
			from rq_contacts where domain_uuid = $1::uuid order by lower(name)`, dom.uuid)
		if err != nil {
			return err
		}
		defer rows.Close()
		var buf bytes.Buffer
		cw := csv.NewWriter(&buf)
		_ = cw.Write(contactFields)
		for rows.Next() {
			rec := make([]string, 6)
			if err := rows.Scan(&rec[0], &rec[1], &rec[2], &rec[3], &rec[4], &rec[5]); err != nil {
				return err
			}
			for i, v := range rec { // keep spreadsheet programs from running formulas
				if v != "" && strings.ContainsRune("=+-@", rune(v[0])) && i != 2 && i != 3 {
					rec[i] = "'" + v
				}
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="contacts-%s-%s.csv"`, dom.name, time.Now().Format("2006-01-02")))
		_, _ = w.Write(buf.Bytes())
		return nil
	}
	return errMethod
}

// importContacts adds the rows of a CSV file. The first row names the
// columns (name, company, phone, mobile, email, notes; "first name" and
// "last name" are joined). Rows without a name or number, and numbers that
// are already in the address book, are skipped.
func (s *Server) importContacts(ctx context.Context, dom domainRow, p *principal, data []byte) (int, int, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err != nil {
		return 0, 0, badRequest("not a CSV file")
	}
	col := map[string]int{}
	for i, h := range header {
		k := strings.ToLower(strings.TrimSpace(h))
		k = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(k)
		switch k {
		case "name", "fullname", "displayname":
			col["name"] = i
		case "firstname", "givenname":
			col["first"] = i
		case "lastname", "surname", "familyname":
			col["last"] = i
		case "company", "organization", "organisation":
			col["company"] = i
		case "phone", "phonenumber", "telephone", "work", "workphone", "number":
			col["phone"] = i
		case "mobile", "mobilephone", "cell", "cellphone":
			col["mobile"] = i
		case "email", "emailaddress", "mail":
			col["email"] = i
		case "notes", "note", "comment", "comments":
			col["notes"] = i
		}
	}
	if _, ok := col["phone"]; !ok {
		if _, ok := col["mobile"]; !ok {
			return 0, 0, badRequest("the first row must name the columns, with at least name and phone")
		}
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
	known := map[string]bool{}
	rows, err := s.store.Pool().Query(ctx, `select regexp_replace(coalesce(phone, '') || ',' || coalesce(mobile, ''), '[^0-9,]', '', 'g') from rq_contacts where domain_uuid = $1::uuid`, dom.uuid)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			for _, d := range strings.Split(v, ",") {
				if d != "" {
					known[d] = true
				}
			}
		}
	}
	rows.Close()
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	added, skipped := 0, 0
	for n := 0; n < 10000; n++ {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			skipped++
			continue
		}
		c := contact{Name: get(rec, "name"), Company: get(rec, "company"), Phone: get(rec, "phone"), Mobile: get(rec, "mobile"),
			Email: get(rec, "email"), Notes: get(rec, "notes")}
		if c.Name == "" {
			c.Name = strings.TrimSpace(get(rec, "first") + " " + get(rec, "last"))
		}
		if c.clean() != nil || known[digitsOf(c.Phone)] && c.Phone != "" || known[digitsOf(c.Mobile)] && c.Mobile != "" {
			skipped++
			continue
		}
		if _, err := tx.Exec(ctx, `insert into rq_contacts (contact_uuid, domain_uuid, name, company, phone, mobile, email, notes, created_by, insert_date, update_date)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())`, newUUID(), dom.uuid, c.Name, c.Company, c.Phone, c.Mobile, c.Email, c.Notes, p.key()); err != nil {
			return 0, 0, err
		}
		for _, d := range []string{digitsOf(c.Phone), digitsOf(c.Mobile)} {
			if d != "" {
				known[d] = true
			}
		}
		added++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return added, skipped, nil
}
