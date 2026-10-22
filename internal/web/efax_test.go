package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/fax"
	"github.com/Cal4Care-Developers/RingQswitch/internal/media"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// faxPNG is a short document to fax.
func faxPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 1728, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 1728; x++ {
			c := color.Gray{Y: 255}
			if y > 60 && (x/48+y/24)%2 == 0 {
				c.Y = 0
			}
			img.SetGray(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// faxLine runs a fax terminal on a test phone's RTP, in real time.
func faxLine(t *testing.T, conn *net.UDPConn, remote netip.AddrPort, term *fax.Terminal, limit time.Duration) {
	t.Helper()
	frames := make(chan []int16, 64)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buf := make([]byte, 1500)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if err != nil || n < 12 || buf[1]&0x7F != 0 {
				continue
			}
			pcm := make([]int16, n-12)
			for i, u := range buf[12:n] {
				pcm[i] = media.UlawToLinear(u)
			}
			select {
			case frames <- pcm:
			case <-stop:
				return
			}
		}
	}()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(limit)
	out := make([]int16, 160)
	payload := make([]byte, 160)
	for seq := 0; !term.Done(); seq++ {
		if time.Now().After(deadline) {
			t.Fatalf("fax line: no result after %v", limit)
		}
		<-tick.C
	drain:
		for {
			select {
			case f := <-frames:
				term.Receive(f)
			default:
				break drain
			}
		}
		term.Transmit(out)
		for i, v := range out {
			payload[i] = media.LinearToUlaw(v)
		}
		_ = testua.SendRTP(conn, remote, 0, uint16(seq), uint32(seq*160), payload)
	}
}

func (r *rig) upload(path string, data []byte) (int, map[string]any) {
	r.t.Helper()
	req, _ := http.NewRequest("POST", r.base+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-RQ", "1")
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// TestEFax: a document goes out through the trunk of the DID it is sent
// from and the far fax machine gets it; a fax to the DID of a fax extension
// lands in the inbox as a PDF; a busy number fails and can be sent again.
func TestEFax(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time fax calls take about a minute")
	}
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	gw := r.mustCall("POST", d+"gateways", map[string]any{"gateway": "Carrier", "proxy": r.carrier.Local.String(), "register": "false"})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6531590000", "gateway_uuid": gw["gateway_uuid"], "destination_description": "Main"})
	fx := r.mustCall("POST", d+"faxes", map[string]any{"fax_name": "Office fax", "fax_extension": "3290", "fax_caller_id_number": "6531590001"})
	r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6531590001", "gateway_uuid": gw["gateway_uuid"],
		"action": "transfer:3290 XML " + domain})
	if fx["fax_uuid"] == nil {
		t.Fatalf("fax extension: %v", fx)
	}
	// The fax DID answers as a fax machine itself (RingQ's form).
	didXML := func() string {
		t.Helper()
		var x string
		if err := r.conn.QueryRow(context.Background(), `select dialplan_xml from v_dialplans d join v_destinations n on n.dialplan_uuid = d.dialplan_uuid
			where n.destination_number = '6531590001'`).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	x := didXML()
	for _, want := range []string{`<extension name="6531590001" continue="false" uuid="`, `field="${destination_number}" expression="^((\+)?6531590001)$"`,
		`data="fax_direction=in"`, `data="domain_uuid=` + domainUUID + `"`, `data="fax_ident=6531590001"`, `data="fax_enable_t38=true"`,
		`data="fax_file=${fax_dir}/` + domainUUID + `/inbox/${uuid}.pdf"`, `application="stop_record_session"`, `application="rxfax" data="${fax_file}"`} {
		if !strings.Contains(x, want) {
			t.Fatalf("fax DID dialplan lacks %s:\n%s", want, x)
		}
	}
	// Changing the fax extension rewrites its DIDs.
	r.mustCall("PUT", d+"faxes/"+fx["fax_uuid"].(string), map[string]any{"fax_caller_id_number": "6531590009"})
	if x := didXML(); !strings.Contains(x, `data="fax_ident=6531590009"`) {
		t.Fatalf("DID dialplan after the fax extension changed:\n%s", x)
	}
	r.mustCall("PUT", d+"faxes/"+fx["fax_uuid"].(string), map[string]any{"fax_caller_id_number": "6531590001"})
	meta := r.mustCall("GET", d+"meta", nil)
	offered := false
	for _, x := range meta["destinations"].([]any) {
		m := x.(map[string]any)
		offered = offered || (m["group"] == "Fax" && m["value"] == "transfer:3290 XML "+domain)
	}
	if !offered {
		t.Fatal("the fax extension is not offered as a destination (Call Flow, Others)")
	}
	r.mustCall("PUT", d+"fax/settings", map[string]string{"fax_ident": "+65 3159 0000", "fax_header": "ACME PTE LTD"})
	ov := r.mustCall("GET", d+"fax", nil)
	if s := ov["settings"].(map[string]any); s["header"] != "ACME PTE LTD" || s["ident"] != "+65 3159 0000" {
		t.Fatalf("settings: %v", s)
	}
	nums := ov["numbers"].([]any)
	if len(nums) != 2 || nums[0].(map[string]any)["trunk"] != "Carrier" || nums[1].(map[string]any)["fax"] != "3290" {
		t.Fatalf("numbers: %v", nums)
	}

	// Refused: a number that is not the domain's, or no document.
	doc := faxPNG(t)
	if c, out := r.upload(d+"fax/send?from=6599999999&to=6588880000&name=a.png", doc); c != 400 || !strings.Contains(out["error"].(string), "number to send from") {
		t.Fatalf("send from a foreign number: %d %v", c, out)
	}
	if c, _ := r.upload(d+"fax/send?from=6531590000&to=6588880000&name=a.txt", []byte("hello")); c != 400 {
		t.Fatalf("send a text file: %d", c)
	}

	// 1. Out: the carrier's fax machine answers and receives the page.
	c, out := r.upload(d+"fax/send?"+url.Values{"from": {"6531590000"}, "to": {"6588880000"}, "name": {"letter.png"}}.Encode(), doc)
	if c != 200 || out["pages"] != float64(1) {
		t.Fatalf("send: %d %v", c, out)
	}
	job := out["jobs"].([]any)[0].(string)
	in, err := r.carrier.WaitInvite(10 * time.Second)
	if err != nil {
		t.Fatalf("the fax was not dialled through the trunk: %v", err)
	}
	// The caller ID is the DID (in From, or P-Asserted-Identity as the trunk is set).
	if !strings.Contains(in.Invite.RequestURI.String(), "6588880000") || !strings.Contains(in.Invite.String(), "6531590000") {
		t.Fatalf("fax call: %s\n%s", in.Invite.RequestURI.String(), in.Invite.String())
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	machine := fax.NewReceiver(fax.Options{Ident: "+6588880000"})
	faxLine(t, in.RTP, in.Remote, machine, 60*time.Second)
	if res := machine.Result(); !res.Success || res.Pages != 1 || res.RemoteID != "+65 3159 0000" {
		t.Fatalf("far machine: %+v", res)
	}
	r.waitCount("sent fax", 1, `select count(*) from fax_jobs where id = $1 and status = 'sent' and pages = 1`, job)

	// 2. In: the carrier sends a fax to the fax DID.
	pages, err := fax.LoadDocument(writeTemp(t, "in.png", doc))
	if err != nil {
		t.Fatal(err)
	}
	callDone := make(chan *testua.Call, 1)
	go func() { c, _ := r.carrier.Dial("6531590001", []int{0}, 10*time.Second, false); callDone <- c }()
	call := <-callDone
	if call == nil || call.Final == nil || call.Final.StatusCode != 200 {
		t.Fatalf("fax DID call: %+v", call)
	}
	sender := fax.NewSender(pages, fax.Options{Ident: "+6577770000", Header: "SENDER CO"})
	faxLine(t, call.RTP, call.RemoteRTP, sender, 60*time.Second)
	if res := sender.Result(); !res.Success || res.RemoteID != "6531590001" {
		t.Fatalf("sender: %+v", res)
	}
	r.waitCount("received fax", 1, `select count(*) from fax_jobs where direction = 'in' and status = 'received' and pages = 1
		and to_number = '6531590001' and from_number = 'carrier'`)
	var inbox []map[string]any
	r.call("GET", d+"fax/jobs?direction=in", nil, &inbox)
	if len(inbox) != 1 || inbox[0]["has_file"] != "true" || inbox[0]["file_path"] != nil {
		t.Fatalf("inbox: %v", inbox)
	}
	resp, err := r.client.Get(r.base + d + "fax/jobs/" + inbox[0]["id"].(string) + "/file")
	if err != nil {
		t.Fatal(err)
	}
	pdf, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.HasPrefix(pdf, []byte("%PDF")) || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("inbox file: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// 3. Busy: failed with the reason; sent again on request.
	c, out = r.upload(d+"fax/send?from=6531590000&to=6588881111&name=b.png", doc)
	if c != 200 {
		t.Fatalf("send: %d %v", c, out)
	}
	busy := out["jobs"].([]any)[0].(string)
	in, err = r.carrier.WaitInvite(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Reject(486)
	r.waitCount("busy fax", 1, `select count(*) from fax_jobs where id = $1 and status = 'failed' and error_reason like 'Busy%'`, busy)
	r.mustCall("POST", d+"fax/jobs/"+busy+"/retry", nil)
	in, err = r.carrier.WaitInvite(10 * time.Second)
	if err != nil {
		t.Fatalf("not sent again: %v", err)
	}
	_ = in.Reject(486)
	r.waitCount("busy again", 1, `select count(*) from fax_jobs where id = $1 and status = 'failed'`, busy)
	ov = r.mustCall("GET", d+"fax", nil)
	if c := ov["counts"].(map[string]any); c["sent"] != float64(1) || c["received"] != float64(1) || c["failed"] != float64(1) {
		t.Fatalf("counts: %v", c)
	}
	r.mustCall("DELETE", d+"fax/jobs/"+busy, nil)
	if n := r.count(`select count(*) from fax_jobs`); n != 2 {
		t.Fatalf("%d jobs after deleting one", n)
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestEFaxExtensionDID: a DID sent to eFax for an extension (Call Flow →
// Others → an extension) answers as a fax machine for it; sent to the
// extension itself, it rings the extension.
func TestEFaxExtensionDID(t *testing.T) {
	r := newRig(t)
	r.mustCall("POST", "login", map[string]string{"user": "admin", "password": "Web-Test-Pass"})
	d := "d/" + domain + "/"
	meta := r.mustCall("GET", d+"meta", nil)
	offered := ""
	for _, x := range meta["destinations"].([]any) {
		m := x.(map[string]any)
		if m["group"] == "eFax" && strings.HasPrefix(m["label"].(string), "1000") {
			offered = m["value"].(string)
		}
	}
	if offered != "efax:1000 XML "+domain {
		t.Fatalf("extension 1000 is not offered as eFax: %q", offered)
	}
	did := r.mustCall("POST", d+"dids", map[string]any{"destination_number": "6531591111", "action": offered})
	xml := func() string {
		t.Helper()
		var x string
		if err := r.conn.QueryRow(context.Background(), `select dialplan_xml from v_dialplans d join v_destinations n on n.dialplan_uuid = d.dialplan_uuid
			where n.destination_number = '6531591111'`).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	x := xml()
	for _, want := range []string{`data="fax_direction=in"`, `data="fax_extension=1000"`, `data="fax_ident=6531591111"`,
		`data="fax_file=${fax_dir}/` + domainUUID + `/inbox/${uuid}.pdf"`, `application="rxfax" data="${fax_file}"`} {
		if !strings.Contains(x, want) {
			t.Fatalf("eFax DID dialplan lacks %s:\n%s", want, x)
		}
	}
	if strings.Contains(x, "fax_uuid=") || strings.Contains(x, `application="transfer"`) || strings.Contains(x, `application="efax"`) {
		t.Fatalf("eFax DID dialplan:\n%s", x)
	}
	got := r.mustCall("GET", d+"dids/"+did["destination_uuid"].(string), nil)
	if got["action"] != offered {
		t.Fatalf("the DID's action reads back as %v", got["action"])
	}
	// The same extension for calls: a plain transfer.
	r.mustCall("PUT", d+"dids/"+did["destination_uuid"].(string), map[string]any{"action": "transfer:1000 XML " + domain})
	if x := xml(); strings.Contains(x, "rxfax") || !strings.Contains(x, `application="transfer" data="1000 XML `+domain+`"`) {
		t.Fatalf("DID for calls:\n%s", x)
	}
}
