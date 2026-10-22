package core_test

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/fax"
	"github.com/Cal4Care-Developers/RingQswitch/internal/sdp"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// t38Offer is a carrier gateway's re-INVITE body: T.38 only, the audio
// declined.
func t38Offer(port int) []byte {
	return []byte(fmt.Sprintf("v=0\r\no=gw 1 2 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n"+
		"m=audio 0 RTP/AVP 8\r\nm=image %d udptl t38\r\na=T38FaxVersion:0\r\na=T38MaxBitRate:14400\r\n"+
		"a=T38FaxRateManagement:transferredTCF\r\na=T38FaxMaxBuffer:262\r\na=T38FaxMaxDatagram:176\r\n"+
		"a=T38FaxUdpEC:t38UDPRedundancy\r\n", port))
}

// switchToT38 sends the re-INVITE to T.38 and returns where our UDPTL is.
func switchToT38(t *testing.T, d *testua.Call, conn *net.UDPConn) netip.AddrPort {
	t.Helper()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	resp, err := d.Request("INVITE", "application/sdp", t38Offer(port))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("re-INVITE to T.38: %v %v", resp, err)
	}
	if err := d.AckFor(resp); err != nil {
		t.Fatal(err)
	}
	ans, err := sdp.Parse(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := ans.Image()
	if img == nil {
		t.Fatalf("no T.38 in the answer:\n%s", resp.Body)
	}
	if a, _ := ans.Audio(); a != nil {
		t.Fatalf("audio not declined:\n%s", resp.Body)
	}
	tt := img.T38()
	if tt.Version != 0 || tt.UDPEC != "t38UDPRedundancy" || tt.RateManagement != "transferredTCF" {
		t.Fatalf("answer %+v:\n%s", tt, resp.Body)
	}
	return netip.AddrPortFrom(netip.MustParseAddr(ans.Conn(img)), uint16(img.Port))
}

// spandspT38 runs spandsp's T.38 terminal (faxpeer t38send/t38receive) on
// a UDP socket, in real time: the carrier's fax gateway.
func spandspT38(t *testing.T, peer string, args []string, conn *net.UDPConn, remote netip.AddrPort, limit time.Duration) string {
	t.Helper()
	cmd := exec.Command(peer, args...)
	cmd.Env = append(os.Environ(), "FAXPEER_MODEMS=3", "FAXPEER_IDENT=+6563401007")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill() //nolint:errcheck
	in := make(chan []byte, 256)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buf := make([]byte, 2048)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := conn.Read(buf)
			if err != nil {
				continue
			}
			select {
			case in <- append([]byte(nil), buf[:n]...):
			case <-stop:
				return
			}
		}
	}()
	r := bufio.NewReader(stdout)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.Now().Add(limit)
	var lb [2]byte
	for time.Now().Before(deadline) && !strings.Contains(stderr.String(), "RESULT") {
		<-tick.C
		var w bytes.Buffer
	drain:
		for {
			select {
			case d := <-in:
				binary.Write(&w, binary.LittleEndian, uint16(len(d))) //nolint:errcheck
				w.Write(d)
			default:
				break drain
			}
		}
		w.Write([]byte{0, 0})
		if _, err := stdin.Write(w.Bytes()); err != nil {
			break
		}
		for {
			if _, err := io.ReadFull(r, lb[:]); err != nil {
				return stderr.String()
			}
			n := int(binary.LittleEndian.Uint16(lb[:]))
			if n == 0 {
				break
			}
			d := make([]byte, n)
			if _, err := io.ReadFull(r, d); err != nil {
				return stderr.String()
			}
			_, _ = conn.WriteToUDPAddrPort(d, remote)
		}
	}
	stdin.Close()
	return stderr.String()
}

func writeTIFF(t *testing.T, doc, path string) {
	t.Helper()
	pages, err := fax.LoadDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := fax.WriteTIFF(f, pages); err != nil {
		t.Fatal(err)
	}
}

// The call from the log of a real failure: txfax to a carrier whose fax
// gateway hears the far end's CED and re-INVITEs to T.38.
func TestFaxCarrierSwitchesToT38(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time fax calls")
	}
	peer := buildFaxPeer(t)
	dir := t.TempDir()
	doc := filepath.Join(dir, "doc.pbm")
	faxPBM(t, doc)
	inbox := filepath.Join(dir, "inbox", "fax.pdf")
	dp := `<include><context name="default">
  <extension name="fax in"><condition field="destination_number" expression="^3290$">
    <action application="set" data="fax_ident=+6560000001"/>
    <action application="answer"/>
    <action application="rxfax" data="` + inbox + `"/>
    <action application="hangup"/>
  </condition></extension>
</context></include>`
	users := fmt.Sprintf(`<include><user id="1000"><params><param name="password" value="%s"/></params></user></include>`, testPassword)
	h := newHarnessWith(t, "", users, dp, nil)
	sub := h.sw.Events().Subscribe(64, func(e *core.Event) bool { return strings.HasPrefix(e.Subclass(), "spandsp::") })
	defer sub.Close()

	t.Run("txfax", func(t *testing.T) {
		carrier, err := testua.NewAt("carrier", "", netip.AddrPort{}, netip.MustParseAddr("127.0.0.1"))
		if err != nil {
			t.Fatal(err)
		}
		defer carrier.Close()
		carrier.Server = h.addr
		done := make(chan string, 1)
		go func() {
			done <- h.sw.API(fmt.Sprintf("originate {fax_ident=6569142668,fax_header=RINGQ,ignore_early_media=true}sip:63401007@%s &txfax(%s)", carrier.Local, doc))
		}()
		in, err := carrier.WaitInvite(5 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := in.Answer(8); err != nil {
			t.Fatal(err)
		}
		// The far end's CED for a moment, then the gateway's re-INVITE.
		for i := 0; i < 50; i++ {
			_ = testua.SendRTP(in.RTP, in.Remote, 8, uint16(i), uint32(i*160), alawTone(2100, i))
			time.Sleep(20 * time.Millisecond)
		}
		udptl, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer udptl.Close()
		remote := switchToT38(t, in.Dialog(), udptl)
		log := spandspT38(t, peer, []string{"t38receive", filepath.Join(dir, "rx.tif")}, udptl, remote, 90*time.Second)
		if !strings.Contains(log, "RESULT 0 ") {
			t.Fatalf("spandsp did not receive the fax: %s", lastLines(log, 15))
		}
		e := waitEvent(t, sub, "spandsp::txfaxresult")
		if e.Get("fax-success") != "1" || e.Get("fax-t38-status") != "on" {
			t.Fatalf("txfax: %s", e.Plain())
		}
		if name := e.Get("Channel-Name"); !strings.HasPrefix(name, "ringq/external/63401007@") {
			t.Fatalf("channel name %q", name)
		}
		<-done
	})

	t.Run("rxfax", func(t *testing.T) {
		alice := h.phone("1000")
		call, err := alice.Dial("3290", []int{8}, 5*time.Second, false)
		if err != nil || call.Final.StatusCode != 200 {
			t.Fatalf("fax call failed: %v", err)
		}
		time.Sleep(time.Second) // our CED starts
		udptl, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer udptl.Close()
		remote := switchToT38(t, call, udptl)
		tif := filepath.Join(dir, "tx.tif")
		writeTIFF(t, doc, tif)
		log := spandspT38(t, peer, []string{"t38send", tif}, udptl, remote, 90*time.Second)
		if !strings.Contains(log, "RESULT 0 ") {
			t.Fatalf("spandsp did not send the fax: %s", lastLines(log, 15))
		}
		e := waitEvent(t, sub, "spandsp::rxfaxresult")
		if e.Get("fax-success") != "1" || e.Get("fax-t38-status") != "on" || e.Get("fax-remote-station-id") != "+6563401007" {
			t.Fatalf("rxfax: %s", e.Plain())
		}
		got, err := os.ReadFile(inbox)
		if err != nil || !bytes.HasPrefix(got, []byte("%PDF")) {
			t.Fatalf("inbox file: %v", err)
		}
	})
}
