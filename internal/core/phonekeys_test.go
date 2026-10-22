package core_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// The desk phone dial codes as the web interface installs them.
const deskCodes = `
  <extension name="own-voicemail" continue="false">
    <condition field="${user_name}" expression="^.+$"/>
    <condition field="${cond(${destination_number} == ${user_name} ? own : other)}" expression="^own$">
      <action application="answer" data=""/>
      <action application="voicemail" data="check default ${domain_name} ${user_name}"/>
    </condition>
  </extension>
  <extension name="call-return"><condition field="destination_number" expression="^\*69$">
    <action application="rq_call_return" data=""/></condition></extension>
  <extension name="redial"><condition field="destination_number" expression="^(redial|\*870)$">
    <action application="rq_redial" data=""/></condition></extension>
  <extension name="dnd-on"><condition field="destination_number" expression="^\*78$">
    <action application="rq_dnd" data="on"/></condition></extension>
  <extension name="dnd-off"><condition field="destination_number" expression="^\*79$">
    <action application="rq_dnd" data="off"/></condition></extension>
  <extension name="dnd-toggle"><condition field="destination_number" expression="^\*77$">
    <action application="rq_dnd" data="toggle"/></condition></extension>
  <extension name="portal-dnd"><condition field="destination_number" expression="^\*76$">
    <action application="set" data="enabled=true"/><action application="lua" data="do_not_disturb.lua"/></condition></extension>
  <extension name="agent-status-number"><condition field="destination_number" expression="^\*26(\d{1,3})$">
    <action application="rq_agent_status" data="aux $1"/></condition></extension>
  <extension name="pickup-ext"><condition field="destination_number" expression="^\*8(\d{2,10})$">
    <action application="rq_pickup" data="$1"/></condition></extension>
  <extension name="local_extension" continue="true">`

func TestDeskPhoneCodes(t *testing.T) {
	vmDir := ""
	dp := "<include>" + strings.Replace(portalContext("a.example"), `<extension name="local_extension" continue="true">`, deskCodes, 1) + "</include>"
	h := newHarnessWith(t, "", tenantUsers(nil), dp, func(cfg *config.Config) {
		vmDir = filepath.Join(cfg.Dir, "vm")
		cfg.Settings.VoicemailDir = vmDir
		cfg.Features.Voicemails["1001@a.example"] = &config.Voicemail{UUID: "vm-1001", Domain: "a.example", ID: "1001", Enabled: true}
		cfg.Features.Agents["agent-1000"] = &config.Agent{UUID: "agent-1000", Domain: "a.example", Name: "1000",
			Contact: "user/1000@a.example", Extension: "1000", Status: config.AgentLoggedOut}
		cfg.Features.AuxCodes = append(cfg.Features.AuxCodes, config.AuxCode{UUID: "aux-1", Domain: "a.example", Name: "Lunch"},
			config.AuxCode{UUID: "aux-2", Domain: "a.example", Name: "Meeting"})
	})
	p0, p1, p2 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	run := func(ua *testua.UA, code string) {
		t.Helper()
		c, err := ua.Dial(code, nil, 5*time.Second, false)
		if err != nil || c.Final == nil || c.Final.StatusCode != 200 {
			t.Fatalf("%s: %+v %v", code, c, err)
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = c.Bye()
	}
	dnd := func() string { return h.sw.UserVar("1002", "a.example", "do_not_disturb") }

	// Do not disturb: *78 on, *79 off, *77 toggle; the RingQ portal's script too.
	run(p2, "*78")
	h.waitFor("DND on", func() bool { return dnd() == "true" })
	if c, _ := p0.Dial("1002", nil, 5*time.Second, false); c == nil || c.Final == nil || c.Final.StatusCode != 486 {
		t.Fatalf("a call to an extension in DND: %+v", c)
	}
	run(p2, "*79")
	h.waitFor("DND off", func() bool { return dnd() == "false" })
	run(p2, "*77")
	h.waitFor("DND toggled on", func() bool { return dnd() == "true" })
	run(p2, "*77")
	h.waitFor("DND toggled off", func() bool { return dnd() == "false" })
	run(p2, "*76")
	h.waitFor("DND on by do_not_disturb.lua", func() bool { return dnd() == "true" })
	run(p2, "*79")
	h.waitFor("DND off", func() bool { return dnd() == "false" })

	// *26N: 1 Available, 2 the first aux code.
	run(p0, "*262")
	h.waitFor("on Lunch", func() bool {
		st, aux := agentStatus(h, "agent-1000")
		return st == config.AgentOnBreak && aux == "Lunch"
	})
	run(p0, "*261")
	h.waitFor("available", func() bool { st, _ := agentStatus(h, "agent-1000"); return st == config.AgentAvailable })

	// *69 calls back the last caller; *870 redials.
	done := dial(p0, "1002")
	in, err := p2.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Reject(486)
	<-done
	done = dial(p2, "*69")
	if in, err = p0.WaitInvite(5 * time.Second); err != nil {
		t.Fatal("*69 did not call back 1000:", err)
	}
	_ = in.Reject(486)
	<-done
	done = dial(p0, "*870")
	if in, err = p2.WaitInvite(5 * time.Second); err != nil {
		t.Fatal("*870 did not redial 1002:", err)
	}
	_ = in.Reject(486)
	<-done

	// *8<ext> picks up a call ringing that extension.
	done = dial(p0, "1002")
	if in, err = p2.WaitInvite(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	_ = in.Ring()
	picker, err := p1.Dial("*81002", nil, 5*time.Second, false)
	if err != nil || picker.Final.StatusCode != 200 {
		t.Fatalf("*81002: %+v %v", picker, err)
	}
	if c := <-done; c == nil || c.Final == nil || c.Final.StatusCode != 200 {
		t.Fatalf("the caller was not connected by the pickup: %+v", c)
	} else {
		_, _ = c.Bye()
	}
	if _, err := p2.WaitRequest("CANCEL", 3*time.Second); err != nil {
		t.Fatal("the picked-up phone kept ringing:", err)
	}
	h.waitChannels(0)

	// Dialing your own extension: voicemail. 7 deletes the message.
	box := filepath.Join(vmDir, "a.example", "1001")
	if err := os.MkdirAll(box, 0o755); err != nil {
		t.Fatal(err)
	}
	tone := make([]int16, 8000)
	for i := range tone {
		tone[i] = int16(4000 * ((i / 10) % 2))
	}
	if err := writeWAV(filepath.Join(box, "msg_m1.wav"), tone); err != nil {
		t.Fatal(err)
	}
	call, err := p1.Dial("1001", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("own extension: %+v %v", call, err)
	}
	time.Sleep(700 * time.Millisecond)
	for i := 0; i < 3; i++ { // 7, end of event
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 101, uint16(10+i), 8000, []byte{7, 0x80, 0, 160})
	}
	h.waitFor("message deleted", func() bool { _, err := os.Stat(filepath.Join(box, "msg_m1.wav")); return os.IsNotExist(err) })
	_, _ = call.Bye()
}

// writeWAV writes 8 kHz 16-bit mono samples.
func writeWAV(path string, pcm []int16) error {
	data := make([]byte, 44+2*len(pcm))
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+2*len(pcm)))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 8000)
	binary.LittleEndian.PutUint32(data[28:], 16000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(2*len(pcm)))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(v))
	}
	return os.WriteFile(path, data, 0o644)
}
