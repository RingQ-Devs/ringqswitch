package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type vars map[string]string

func (v vars) Var(n string) string    { return v[n] }
func (v vars) SetVar(n, val string)   { v[n] = val }
func (v vars) Expand(s string) string { return s }

func TestLoadSampleConfig(t *testing.T) {
	cfg, err := Load("../../conf")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("sample config has warnings: %v", cfg.Warnings)
	}
	s := cfg.Settings
	if s.SIPPort != 5060 || !s.EnableTCP || s.SIPBindIP != "" || s.SIPIP == "" || s.Domain != s.SIPIP {
		t.Fatalf("unexpected sip settings %+v", s)
	}
	if s.ESLPassword == "" || s.CDRPath == "" || !strings.HasSuffix(s.CDRPath, "log/cdr.csv") {
		t.Fatalf("unexpected settings %+v", s)
	}
	u := cfg.Directory.Lookup("1000")
	if u == nil || u.Password != "ChangeMe-1234" || u.Vars["user_context"] != "default" {
		t.Fatalf("user 1000 not loaded correctly: %+v", u)
	}
	if cfg.Directory.Lookup("9999") != nil {
		t.Fatal("unexpected user")
	}
	for _, c := range []string{"default", "public"} {
		if cfg.Dialplan.Contexts[c] == nil {
			t.Fatalf("missing context %s", c)
		}
	}
	acts, _, err := cfg.Dialplan.Hunt("default", vars{"destination_number": "1001"}, time.Now())
	if err != nil || len(acts) == 0 {
		t.Fatalf("hunt failed: %v %v", acts, err)
	}
	found := false
	for _, a := range acts {
		if a.App == "bridge" && a.Data == "user/1001" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected bridge to user/1001 in %+v", acts)
	}
	acts, _, _ = cfg.Dialplan.Hunt("default", vars{"destination_number": "5000"}, time.Now())
	if len(acts) < 2 || acts[2].App != "play_and_get_digits" || !strings.Contains(acts[2].Data, "%(500,500,480,620);loops=1") {
		t.Fatalf("global variables not expanded in ivr: %+v", acts)
	}
}

func writeConf(t *testing.T, main, directory, dialplan string) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range map[string]string{
		"ringqswitch.xml":      main,
		"directory/users.xml":  directory,
		"dialplan/default.xml": dialplan,
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const minimalMain = `<configuration>
  <vars><var name="a" value="1.2.3.4"/><var name="b" value="$${a}"/></vars>
  <settings>
    <param name="sip-ip" value="$${b}"/>
    <param name="event-socket-listen" value=""/>
    <param name="cdr-csv" value=""/>
  </settings>
  <gateways>
    <gateway name="gw1"><param name="proxy" value="sip.example.com:5070"/><param name="username" value="u"/><param name="register" value="true"/></gateway>
  </gateways>
  <acl><node cidr="10.1.0.0/16" context="trusted"/><node cidr="192.0.2.7"/></acl>
</configuration>`

func TestVarsGatewaysACL(t *testing.T) {
	dir := writeConf(t, minimalMain,
		`<include><user id="200"><params><param name="a1-hash" value="abc"/></params></user></include>`,
		`<include><context name="default"/></include>`)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Settings
	if s.SIPBindIP != "1.2.3.4" || s.SIPIP != "1.2.3.4" || s.RTPIP != "1.2.3.4" {
		t.Fatalf("chained vars not expanded: %+v", s)
	}
	if s.ESLListen != "" || s.CDRPath != "" {
		t.Fatal("empty values should disable features")
	}
	if len(cfg.Gateways) != 1 {
		t.Fatal("gateway not loaded")
	}
	g := cfg.Gateways[0]
	if g.Realm != "sip.example.com" || g.FromUser != "u" || g.FromDomain != "sip.example.com" || g.Context != "public" || !g.Register {
		t.Fatalf("gateway defaults: %+v", g)
	}
	if e := cfg.MatchACL(netip.MustParseAddr("10.1.2.3")); e == nil || e.Context != "trusted" {
		t.Fatal("acl cidr match")
	}
	if e := cfg.MatchACL(netip.MustParseAddr("192.0.2.7")); e == nil || e.Context != "public" {
		t.Fatal("acl host match")
	}
	if cfg.MatchACL(netip.MustParseAddr("8.8.8.8")) != nil {
		t.Fatal("unexpected acl match")
	}
}

func TestConfigErrors(t *testing.T) {
	cases := map[string][3]string{
		"no password":          {minimalMain, `<include><user id="1"/></include>`, `<include/>`},
		"dup user":             {minimalMain, `<include><user id="1"><params><param name="password" value="x"/></params></user><user id="1"><params><param name="password" value="y"/></params></user></include>`, `<include/>`},
		"bad regex":            {minimalMain, `<include/>`, `<context name="x"><extension name="e"><condition field="f" expression="(("/></extension></context>`},
		"esl without password": {`<configuration><settings/></configuration>`, `<include/>`, `<include/>`},
		"bad codec":            {strings.Replace(minimalMain, "<settings>", `<settings><param name="codec-prefs" value="G729"/>`, 1), `<include/>`, `<include/>`},
		"bad bool":             {strings.Replace(minimalMain, "<settings>", `<settings><param name="enable-tcp" value="maybe"/>`, 1), `<include/>`, `<include/>`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeConf(t, c[0], c[1], c[2])); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
