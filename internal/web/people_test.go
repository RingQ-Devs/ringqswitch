package web_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestManagerRoleAndRolesBoard(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1001"), map[string]any{"web_role": "owner", "web_password": "Pass-1001-x"})
	owner := r.session("1001", "Pass-1001-x")

	var list []map[string]any
	if code := owner.call("GET", d+"roles", nil, &list); code != 200 || len(list) < 3 {
		t.Fatalf("roles: %d %v", code, list)
	}
	role := func(ext string) map[string]any {
		var l []map[string]any
		owner.call("GET", d+"roles", nil, &l)
		for _, p := range l {
			if p["extension"] == ext {
				return p
			}
		}
		t.Fatalf("%s not on the roles board", ext)
		return nil
	}
	if p := role("1001"); p["role"] != "owner" || p["you"] != true {
		t.Fatalf("1001: %v", p)
	}
	if p := role("1002"); p["role"] != "" {
		t.Fatalf("1002 should have no access yet: %v", p)
	}
	var out map[string]any
	// No portal access yet: a password is needed.
	if code := owner.call("PUT", d+"roles/"+r.extensionUUID("1002"), map[string]string{"role": "manager"}, &out); code != 400 {
		t.Fatalf("role without a password: %d", code)
	}
	owner.mustCall("PUT", d+"roles/"+r.extensionUUID("1002"), map[string]string{"role": "manager", "password": "Pass-1002-x"})
	if p := role("1002"); p["role"] != "manager" {
		t.Fatalf("1002: %v", p)
	}
	// Owners cannot change their own role; users cannot use the board.
	if code := owner.call("PUT", d+"roles/"+r.extensionUUID("1001"), map[string]string{"role": "user"}, &out); code != 400 {
		t.Fatalf("own role changed: %d", code)
	}
	mgr := r.session("1002", "Pass-1002-x")
	var me map[string]any
	mgr.call("GET", "me", nil, &me)
	if me["role"] != "manager" {
		t.Fatalf("me: %v", me)
	}
	if code := mgr.call("GET", d+"roles", nil, &out); code != 403 {
		t.Fatalf("manager opened the roles board: %d", code)
	}
	if code := mgr.call("GET", d+"extensions", nil, &out); code != 403 {
		t.Fatalf("manager listed extensions (settings): %d", code)
	}
	// Managers supervise: every agent, monitoring, all reports.
	q := owner.mustCall("POST", d+"queues", map[string]any{"queue_name": "Q", "queue_extension": "3001",
		"agents": []map[string]string{{"extension": "1001"}}})
	agent := q["agents"].([]any)[0].(map[string]any)["call_center_agent_uuid"].(string)
	mgr.mustCall("POST", "agents/"+agent+"/status", map[string]string{"status": "Available"})
	if code := mgr.call("POST", "calls/00000000-0000-0000-0000-000000000000/listen", map[string]string{}, &out); code != 404 {
		t.Fatalf("manager monitoring: %d (want 404 for an unknown call, not 403)", code)
	}
	// A role change signs the person out (their session had the old role).
	owner.mustCall("PUT", d+"roles/"+r.extensionUUID("1002"), map[string]string{"role": "user"})
	if code := mgr.call("GET", "me", nil, &me); code != 401 {
		t.Fatalf("session kept after a role change: %d", code)
	}
	// Back to no access: the login is gone.
	owner.mustCall("PUT", d+"roles/"+r.extensionUUID("1002"), map[string]string{"role": ""})
	if code := r.call("POST", "login", map[string]string{"user": "1002", "password": "Pass-1002-x"}, &out); code == 200 {
		t.Fatal("signed in without portal access")
	}
}

func TestContacts(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1002"), map[string]any{"web_role": "user", "web_password": "Pass-1002-x"})
	r.mustCall("PUT", d+"extensions/"+r.extensionUUID("1000"), map[string]any{"web_role": "user", "web_password": "Pass-1000-x"})
	u2, u3 := r.session("1002", "Pass-1002-x"), r.session("1000", "Pass-1000-x")

	var out map[string]any
	if code := u2.call("POST", d+"contacts", map[string]string{"name": "", "phone": "123"}, &out); code != 400 {
		t.Fatalf("contact without a name: %d", code)
	}
	if code := u2.call("POST", d+"contacts", map[string]string{"name": "X", "phone": "abc"}, &out); code != 400 {
		t.Fatalf("bad phone accepted: %d", code)
	}
	c := u2.mustCall("POST", d+"contacts", map[string]string{"name": "Acme Support", "company": "Acme", "phone": "+65 6123 4567", "email": "help@acme.example"})
	id := c["contact_uuid"].(string)
	var l struct {
		Rows  []map[string]string `json:"rows"`
		Total int                 `json:"total"`
	}
	u3.call("GET", d+"contacts?q=acme", nil, &l)
	if l.Total != 1 || l.Rows[0]["name"] != "Acme Support" || l.Rows[0]["can_edit"] != "false" {
		t.Fatalf("list: %+v", l)
	}
	u3.call("GET", d+"contacts?q=61234", nil, &l)
	if l.Total != 1 {
		t.Fatalf("search by digits: %+v", l)
	}
	// Someone else's contact: users may not change it; its author and admins may.
	if code := u3.call("PUT", d+"contacts/"+id, map[string]string{"name": "Mine now", "phone": "1"}, &out); code != 403 {
		t.Fatalf("other user edited: %d", code)
	}
	u2.mustCall("PUT", d+"contacts/"+id, map[string]string{"name": "Acme Helpdesk", "company": "Acme", "phone": "+65 6123 4567"})
	// Caller name lookup works with national and international forms.
	look := u3.mustCall("GET", d+"contacts/lookup?number=6561234567", nil)
	if look["name"] != "Acme Helpdesk" || look["company"] != "Acme" {
		t.Fatalf("lookup: %v", look)
	}
	look = u3.mustCall("GET", d+"contacts/lookup?number=61234567", nil)
	if look["name"] != "Acme Helpdesk" {
		t.Fatalf("lookup without country code: %v", look)
	}
	// Import (supervisors only): new rows added, duplicates and bad rows skipped.
	csvData := "First Name,Last Name,Company,Phone,Email\nJane,Doe,Doe Ltd,91234567,jane@doe.example\nDup,Row,,+65 6123 4567,\n,,,,\nBob,,Bob Co,=1+2,\n"
	if code := u2.call("POST", d+"contacts/import", nil, &out); code != 403 {
		t.Fatalf("user imported: %d", code)
	}
	req, _ := http.NewRequest("POST", r.base+d+"contacts/import", strings.NewReader(csvData))
	req.Header.Set("X-RQ", "1")
	resp, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body, []byte(`"added":1`)) || !bytes.Contains(body, []byte(`"skipped":3`)) {
		t.Fatalf("import: %d %s", resp.StatusCode, body)
	}
	resp, err = r.client.Get(r.base + d + "contacts/export")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.HasPrefix(string(body), "name,company,phone,mobile,email,notes\n") || !strings.Contains(string(body), "Jane Doe,Doe Ltd,91234567") {
		t.Fatalf("export:\n%s", body)
	}
	if code := u2.call("DELETE", d+"contacts/"+id, nil, &out); code != 200 {
		t.Fatalf("author delete: %d", code)
	}
}
