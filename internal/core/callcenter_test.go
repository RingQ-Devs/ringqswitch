package core_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/config"
	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
	"github.com/Cal4Care-Developers/RingQswitch/internal/testua"
)

// queueConfig adds queue 3000 in a.example served by agents on 1001 and
// 1002, with the given strategy.
func queueConfig(strategy string, tweak func(q *config.Queue, agents []*config.Agent)) func(*config.Config) {
	return func(cfg *config.Config) {
		q := &config.Queue{UUID: "q-sales", Domain: "a.example", Name: "Sales", Extension: "3000", Strategy: strategy,
			MOHSound: "silence", MaxWaitTimeNoAgentReached: 5 * time.Second}
		var agents []*config.Agent
		for i, ext := range []string{"1001", "1002"} {
			a := &config.Agent{UUID: "agent-" + ext, Domain: "a.example", Name: "Agent " + ext,
				Contact: "user/" + ext + "@a.example", Extension: ext, Status: config.AgentAvailable,
				CallTimeout: 4 * time.Second, NoAnswerDelay: 30 * time.Second, BusyDelay: 30 * time.Second, RejectDelay: 30 * time.Second}
			agents = append(agents, a)
			cfg.Features.Agents[a.UUID] = a
			cfg.Features.Tiers = append(cfg.Features.Tiers, config.Tier{QueueUUID: q.UUID, AgentUUID: a.UUID, Level: 1, Position: i + 1})
		}
		cfg.Features.AuxCodes = append(cfg.Features.AuxCodes,
			config.AuxCode{Name: "Lunch", Domain: "a.example"},
			config.AuxCode{Name: "Meeting", Domain: "a.example", BlockDirect: true})
		if tweak != nil {
			tweak(q, agents)
		}
		cfg.Features.Queues[q.UUID] = q
	}
}

// waitAny waits for an INVITE on any of the phones.
func waitAny(t *testing.T, timeout time.Duration, phones ...*testua.UA) (int, *testua.Incoming) {
	t.Helper()
	type res struct {
		i  int
		in *testua.Incoming
	}
	ch := make(chan res, len(phones))
	for i, p := range phones {
		go func(i int, p *testua.UA) {
			if in, err := p.WaitInvite(timeout); err == nil {
				ch <- res{i, in}
			}
		}(i, p)
	}
	select {
	case r := <-ch:
		return r.i, r.in
	case <-time.After(timeout):
		return -1, nil
	}
}

func agentStatus(h *harness, id string) (status, aux string) {
	for _, a := range h.sw.Agents("a.example") {
		if a.UUID == id {
			return a.Status, a.Aux
		}
	}
	return "", ""
}

func TestQueueAgentsAndBreaks(t *testing.T) {
	h := tenantHarness(t, nil, queueConfig("longest-idle-agent", nil))
	caller := h.tenantPhone("1000", "a.example")
	a1, a2 := h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")

	// 1. A caller reaches an agent.
	call, err := caller.Dial("3000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("queue should answer: %v %v", call, err)
	}
	idx, in := waitAny(t, 5*time.Second, a1, a2)
	if in == nil {
		t.Fatal("no agent was offered the call")
	}
	if !strings.Contains(in.Invite.Get("From"), "1000") {
		t.Fatalf("agent should see the caller's number: %s", in.Invite.Get("From"))
	}
	if err := in.Answer(0); err != nil {
		t.Fatal(err)
	}
	h.waitFor("queue call bridged", func() bool {
		q := h.sw.Queues("a.example")
		return len(q) == 1 && len(q[0].Callers) == 1 && q[0].Callers[0].State == "Answered"
	})
	first := []string{"agent-1001", "agent-1002"}[idx]
	_, _ = call.Bye()
	h.waitChannels(0)

	// 2. The agent who answered goes on a lunch break: the next call goes
	//    to the other agent.
	if out := h.sw.API("callcenter_config agent set status " + first + " 'On Break' Lunch"); !strings.HasPrefix(out, "+OK") {
		t.Fatal(out)
	}
	if st, aux := agentStatus(h, first); st != config.AgentOnBreak || aux != "Lunch" {
		t.Fatalf("status %q aux %q", st, aux)
	}
	call, err = caller.Dial("3000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatal("second call", err)
	}
	idx2, in := waitAny(t, 5*time.Second, a1, a2)
	if in == nil || []string{"agent-1001", "agent-1002"}[idx2] == first {
		t.Fatalf("the call landed on the agent on break (idx %d)", idx2)
	}
	_ = in.Answer(0)
	h.waitFor("queue call bridged", func() bool {
		q := h.sw.Queues("a.example")
		return len(q) == 1 && len(q[0].Callers) == 1 && q[0].Callers[0].State == "Answered"
	})
	_, _ = call.Bye()
	h.waitChannels(0)

	// 3. Everybody on break: callers wait; an agent coming back gets it.
	_ = h.sw.SetAgentStatus(core.AgentStatusChange{Agent: "agent-1001", Status: "On Break", Aux: "Lunch", Source: "test"})
	_ = h.sw.SetAgentStatus(core.AgentStatusChange{Agent: "agent-1002", Status: "On Break", Aux: "Lunch", Source: "test"})
	call, err = caller.Dial("3000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatal("third call", err)
	}
	if i, _ := waitAny(t, 1500*time.Millisecond, a1, a2); i >= 0 {
		t.Fatal("a call was offered while every agent was on break")
	}
	if q := h.sw.Queues("a.example"); q[0].Waiting != 1 || q[0].OnBreak != 2 {
		t.Fatalf("queue view: %+v", q[0])
	}
	_ = h.sw.SetAgentStatus(core.AgentStatusChange{Agent: "agent-1002", Status: "Available", Source: "test"})
	idx3, in := waitAny(t, 5*time.Second, a1, a2)
	if idx3 != 1 {
		t.Fatalf("agent 1002 should get the waiting call, got %d", idx3)
	}
	_ = in.Answer(0)
	h.waitFor("queue call bridged", func() bool {
		q := h.sw.Queues("a.example")
		return len(q) == 1 && len(q[0].Callers) == 1 && q[0].Callers[0].State == "Answered"
	})
	_, _ = call.Bye()
	h.waitChannels(0)

	q := h.sw.Queues("a.example")[0]
	if q.AnsweredToday != 3 {
		t.Fatalf("answered today = %d", q.AnsweredToday)
	}
	if out := h.sw.API("callcenter_config agent list"); !strings.Contains(out, "agent-1001|single_box||callback|user/1001@a.example|On Break") {
		t.Fatalf("agent list:\n%s", out)
	}
	if n := h.sw.API("callcenter_config queue count agents 3000@a.example"); strings.TrimSpace(n) != "2" {
		t.Fatalf("count: %q", n)
	}
	logs := h.sw.EventLog("agent", "", 50)
	if len(logs) < 3 || !strings.Contains(logs[0].Text, "Agent 1002 (1002) Available") {
		t.Fatalf("agent status changes should be logged: %+v", logs)
	}
}

func TestQueueRingAllAndAuxBlocksDirect(t *testing.T) {
	h := tenantHarness(t, nil, queueConfig("ring-all", nil))
	caller := h.tenantPhone("1000", "a.example")
	a1, a2 := h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	call, err := caller.Dial("3000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatal(err)
	}
	in1, err := a1.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("ring-all: 1001 not offered", err)
	}
	in2, err := a2.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("ring-all: 1002 not offered", err)
	}
	_ = in1.Ring()
	_ = in2.Ring()
	_ = in2.Answer(0)
	if _, err := a1.WaitRequest("CANCEL", 5*time.Second); err != nil {
		t.Fatal("the other agent should stop ringing", err)
	}
	_, _ = call.Bye()
	h.waitChannels(0)

	// An AUX code with "block direct calls" makes direct calls busy too.
	_ = h.sw.SetAgentStatus(core.AgentStatusChange{Agent: "1001", Domain: "a.example", Status: "On Break", Aux: "Meeting", Source: "test"})
	c2, err := caller.Dial("1001", nil, 5*time.Second, false)
	if err != nil || c2.Final.StatusCode != 486 {
		t.Fatalf("direct call to an agent in a meeting should be busy: %v %v", c2, err)
	}
	// Lunch does not block direct calls.
	_ = h.sw.SetAgentStatus(core.AgentStatusChange{Agent: "1001", Domain: "a.example", Status: "On Break", Aux: "Lunch", Source: "test"})
	done := dial(caller, "1001")
	in, err := a1.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal("direct call during lunch should ring", err)
	}
	answerCall(t, done, in)
}

func TestQueueNoAnswerTimeoutAndFeatureCode(t *testing.T) {
	h := tenantHarness(t, nil, queueConfig("top-down", func(q *config.Queue, agents []*config.Agent) {
		q.MaxWaitTime = 6 * time.Second
		agents[0].CallTimeout = 2 * time.Second
		agents[0].MaxNoAnswer = 1
		agents[1].Status = config.AgentLoggedOut
	}))
	caller := h.tenantPhone("1000", "a.example")
	a1 := h.tenantPhone("1001", "a.example")
	h.tenantPhone("1002", "a.example")
	timeoutDest := h.tenantPhone("1003", "a.example")
	call, err := caller.Dial("3000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatal(err)
	}
	in, err := a1.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Ring() // and never answer
	h.waitFor("agent put on break after max no answer", func() bool {
		st, aux := agentStatus(h, "agent-1001")
		return st == config.AgentOnBreak && aux == "Max no answer"
	})
	// Nobody left: after max wait time the caller goes to the timeout
	// destination of the dialplan (1003).
	in3, err := timeoutDest.WaitInvite(8 * time.Second)
	if err != nil {
		t.Fatal("caller should reach the timeout destination", err)
	}
	_ = in3.Answer(0)
	_, _ = call.Bye()
	h.waitChannels(0)
	if q := h.sw.Queues("a.example")[0]; q.TimeoutToday != 1 {
		t.Fatalf("timeouts today = %d", q.TimeoutToday)
	}

	// *22 toggles the agent's login.
	c2, err := a1.Dial("*22", nil, 5*time.Second, false)
	if err != nil || c2.Final.StatusCode != 200 {
		t.Fatal(err)
	}
	h.waitFor("agent toggled", func() bool { st, _ := agentStatus(h, "agent-1001"); return st == config.AgentLoggedOut })
	_, _ = c2.Bye()
}

func TestVoicemailOnNoAnswer(t *testing.T) {
	vmDir := ""
	h := tenantHarness(t, nil, func(cfg *config.Config) {
		vmDir = filepath.Join(cfg.Dir, "vm")
		cfg.Settings.VoicemailDir = vmDir
		cfg.Features.Voicemails["1001@a.example"] = &config.Voicemail{UUID: "vm-1001", Domain: "a.example", ID: "1001", Enabled: true}
	})
	sub := h.sw.Events().Subscribe(64, func(e *core.Event) bool { return e.Subclass() == "vm::maintenance" })
	defer sub.Close()
	caller := h.tenantPhone("1000", "a.example")
	// 1001 is not registered: the call goes to voicemail.
	call, err := caller.Dial("1001", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatalf("voicemail should answer: %v %v", call, err)
	}
	for i := 0; i < 450; i++ { // the built-in greeting (5 s), the beep, then 3 s of audio
		_ = testua.SendRTP(call.RTP, call.RemoteRTP, 0, uint16(i), uint32(i*160), ulaw(2000, 160))
		time.Sleep(20 * time.Millisecond)
	}
	_, _ = call.Bye()
	select {
	case e := <-sub.C:
		path := e.Get("VM-File-Path")
		if e.Get("VM-User") != "1001" || !strings.HasPrefix(path, vmDir) {
			t.Fatalf("event %s", e.Plain())
		}
		if st, err := os.Stat(path); err != nil || st.Size() < 8000 {
			t.Fatalf("message file: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no voicemail event")
	}
}

func TestConferenceMixes(t *testing.T) {
	h := tenantHarness(t, nil, nil)
	p1, p2, p3 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	var calls []*testua.Call
	for _, p := range []*testua.UA{p1, p2, p3} {
		c, err := p.Dial("4000", nil, 5*time.Second, false)
		if err != nil || c.Final.StatusCode != 200 {
			t.Fatal(err)
		}
		calls = append(calls, c)
	}
	h.waitFor("3 members", func() bool {
		r := h.sw.Conferences("a.example")
		return len(r) == 1 && len(r[0].Members) == 3
	})
	// Member 1 talks; member 2 must hear it.
	loud := ulaw(12000, 160)
	got := false
	for i := 0; i < 100 && !got; i++ {
		_ = testua.SendRTP(calls[0].RTP, calls[0].RemoteRTP, 0, uint16(i), uint32(i*160), loud)
		_, p, err := testua.RecvRTP(calls[1].RTP, 40*time.Millisecond, nil)
		if err == nil && !bytes.Equal(p, ulaw(0, len(p))) {
			got = true
		}
	}
	if !got {
		t.Fatal("member 2 did not hear member 1")
	}
	if out := h.sw.API("conference list"); !strings.Contains(out, "3 member(s)") {
		t.Fatal(out)
	}
	for _, c := range calls {
		_, _ = c.Bye()
	}
	h.waitChannels(0)
	h.waitFor("room closed", func() bool { return len(h.sw.Conferences("")) == 0 })
}

func TestParkAndPickup(t *testing.T) {
	h := tenantHarness(t, nil, nil)
	p0, p1, p2 := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")

	// Park on *5901, retrieve from another phone.
	parked, err := p0.Dial("*5901", nil, 5*time.Second, false)
	if err != nil || parked.Final.StatusCode != 200 {
		t.Fatal(err)
	}
	h.waitFor("parked", func() bool { return len(h.sw.ParkedCalls("a.example")) == 1 })
	retr, err := p2.Dial("*5901", nil, 5*time.Second, false)
	if err != nil || retr.Final.StatusCode != 200 {
		t.Fatal(err)
	}
	h.waitFor("park retrieved", func() bool {
		return len(h.sw.ParkedCalls("a.example")) == 0 && strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2
	})
	_, _ = retr.Bye()
	h.waitChannels(0)

	// 1000 calls 1001; 1002 picks it up with **1001.
	done := dial(p0, "1001")
	in, err := p1.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = in.Ring()
	pick, err := p2.Dial("**1001", nil, 5*time.Second, false)
	if err != nil || pick.Final.StatusCode != 200 {
		t.Fatalf("pickup: %v %v", pick, err)
	}
	if _, err := p1.WaitRequest("CANCEL", 5*time.Second); err != nil {
		t.Fatal("ringing phone should stop", err)
	}
	select {
	case c := <-done:
		if c == nil || c.Final.StatusCode != 200 {
			t.Fatalf("caller should be answered by the pickup: %+v", c)
		}
		_, _ = c.Bye()
	case <-time.After(5 * time.Second):
		t.Fatal("caller not answered")
	}
	h.waitChannels(0)
}

func TestHoldMusicAndAttendedTransfer(t *testing.T) {
	h := tenantHarness(t, nil, nil)
	a, b, c := h.tenantPhone("1000", "a.example"), h.tenantPhone("1001", "a.example"), h.tenantPhone("1002", "a.example")
	// A calls B.
	done := dial(a, "1001")
	inB, err := b.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	call1 := answerCall(t, done, inB)
	h.waitFor("bridged", func() bool { return strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2 })

	// A puts B on hold: B hears music (non-silent RTP from the switch).
	port := call1.RTP.LocalAddr().String()
	_, portStr, _ := strings.Cut(port, ":")
	var p int
	fmt.Sscanf(portStr, "%d", &p)
	if resp, err := call1.Request("INVITE", "application/sdp", holdSDP(p)); err != nil || resp.StatusCode != 200 {
		t.Fatalf("hold re-INVITE: %v %v", resp, err)
	}
	_ = call1.Ack()
	heard := false
	for i := 0; i < 50 && !heard; i++ {
		_, pl, err := testua.RecvRTP(inB.RTP, 100*time.Millisecond, nil)
		if err == nil && !bytes.Equal(pl, ulaw(0, len(pl))) {
			heard = true
		}
	}
	if !heard {
		t.Fatal("held party should hear music on hold")
	}

	// A calls C (consultation), then transfers B to C.
	done2 := dial(a, "1002")
	inC, err := c.WaitInvite(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	call2 := answerCall(t, done2, inC)
	replaces := fmt.Sprintf("%s%%3Bto-tag%%3D%s%%3Bfrom-tag%%3D%s", call2.CallID, call2.ToTag, call2.FromTag)
	resp, err := call1.Request("REFER", "", nil, "Refer-To", "<sip:1002@a.example?Replaces="+replaces+">", "Referred-By", "<sip:1000@a.example>")
	if err != nil || resp.StatusCode != 202 {
		t.Fatalf("REFER: %v %v", resp, err)
	}
	// B and C end up connected: audio from B reaches C.
	h.waitFor("B and C bridged", func() bool {
		chs := h.sw.Channels()
		return len(chs) == 2 && strings.Count(h.sw.API("show channels"), "CS_EXCHANGE_MEDIA") == 2
	})
	got := false
	for i := 0; i < 50 && !got; i++ {
		_ = testua.SendRTP(inB.RTP, inB.Remote, 0, uint16(i), uint32(i*160), ulaw(9000, 160))
		if _, _, err := testua.RecvRTP(inC.RTP, 40*time.Millisecond, nil); err == nil {
			got = true
		}
	}
	if !got {
		t.Fatal("transferred parties are not connected")
	}
}

func holdSDP(port int) []byte {
	s := string(testua.SDP(port, 0))
	return []byte(strings.Replace(s, "a=sendrecv", "a=sendonly", 1) + func() string {
		if strings.Contains(s, "a=sendrecv") {
			return ""
		}
		return "a=sendonly\r\n"
	}())
}

// TestQueueNoAgentAvailable: the only agents are on a break or have no
// phone online. The caller is told nobody is available and leaves for the
// timeout destination (1003, as voicemail would be); the agent without a
// phone is never rung, so it collects no missed calls.
func TestQueueNoAgentAvailable(t *testing.T) {
	h := tenantHarness(t, nil, queueConfig("longest-idle-agent", func(q *config.Queue, agents []*config.Agent) {
		q.MaxWaitTimeNoAgent = 2 * time.Second
		q.MaxWaitTimeNoAgentReached = 0
		agents[0].Status = config.AgentOnBreak // 1001: registered, on a break
		agents[1].MaxNoAnswer = 1              // 1002: Available, no phone online
	}))
	caller := h.tenantPhone("1000", "a.example")
	a1 := h.tenantPhone("1001", "a.example")
	timeoutDest := h.tenantPhone("1003", "a.example")
	call, err := caller.Dial("3000", nil, 5*time.Second, false)
	if err != nil || call.Final.StatusCode != 200 {
		t.Fatal(err)
	}
	// The announcement: speech reaches the caller before it leaves.
	heard := 0
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) && heard < 50 {
		_, p, err := testua.RecvRTP(call.RTP, 100*time.Millisecond, nil)
		if err == nil && !bytes.Equal(p, ulaw(0, len(p))) && !bytes.Equal(p, bytes.Repeat([]byte{0xFF}, len(p))) {
			heard++
		}
	}
	if heard < 50 {
		t.Fatalf("the caller heard %d frames of the no-agent announcement", heard)
	}
	in3, err := timeoutDest.WaitInvite(8 * time.Second)
	if err != nil {
		t.Fatal("the caller should reach the timeout destination", err)
	}
	if _, in := waitAny(t, 300*time.Millisecond, a1); in != nil {
		t.Fatal("the agent on a break was rung")
	}
	_ = in3.Answer(0)
	_, _ = call.Bye()
	h.waitChannels(0)
	for _, a := range h.sw.Agents("a.example") {
		if a.UUID == "agent-1002" && (a.Status != config.AgentAvailable || a.NoAnswerCount != 0 || a.MissedToday != 0) {
			t.Fatalf("the agent with no phone online was rung: %+v", a)
		}
	}
	if q := h.sw.Queues("a.example")[0]; q.TimeoutToday != 1 {
		t.Fatalf("timeouts today = %d", q.TimeoutToday)
	}
}
