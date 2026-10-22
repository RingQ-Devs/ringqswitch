package web

import (
	"strings"
	"time"

	"github.com/Cal4Care-Developers/RingQswitch/internal/core"
)

// The wallboard's "Logged In" follows the portal: signing in logs the
// extension's agent in to its queues (Available, or the status it had
// when the portal logged it out), and signing out logs it out when the
// extension is signed in nowhere else: no other portal session, and no
// phone registered (the browser dialer unregisters as the page signs out;
// a desk phone keeps it in).

var agentLogoutDelay = 5 * time.Second

func (s *Server) agentOf(p *principal) *core.AgentInfo {
	for _, a := range s.sw.Agents(p.Domain) {
		if strings.EqualFold(a.Extension, p.Extension) && strings.EqualFold(a.Domain, p.Domain) {
			return &a
		}
	}
	return nil
}

func (s *Server) registered(p *principal) bool {
	for _, b := range s.sw.Registrar().All() {
		if strings.EqualFold(b.User, p.Extension) && strings.EqualFold(b.Domain, p.Domain) {
			return true
		}
	}
	return false
}

// portalSignedIn reports whether the extension has an open portal session.
func (s *Server) portalSignedIn(p *principal) bool {
	key := strings.ToLower(p.key())
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.sessions {
		if x.p != nil && x.p.Kind == "extension" && now.Before(x.expires) && strings.ToLower(x.p.key()) == key {
			return true
		}
	}
	return false
}

// agentSignedOut runs as an extension signs out of the portal; the check
// runs a few seconds later, when its session is gone and its browser
// dialer has unregistered.
func (s *Server) agentSignedOut(p *principal) {
	if p == nil || p.Kind != "extension" || s.agentOf(p) == nil {
		return
	}
	time.AfterFunc(agentLogoutDelay, func() {
		a := s.agentOf(p)
		// Still signed in in another browser (or again, within the delay),
		// or a phone is registered: the agent stays in.
		if a == nil || a.Status == "Logged Out" || s.portalSignedIn(p) || s.registered(p) {
			return
		}
		if err := s.sw.SetAgentStatus(core.AgentStatusChange{Agent: a.UUID, Domain: p.Domain, Status: "Logged Out", Source: "web sign-out"}); err != nil {
			s.log.Warn("web: agent log out on sign-out failed", "agent", p.key(), "err", err)
			return
		}
		s.mu.Lock()
		s.signedOutAgents[strings.ToLower(p.key())] = a.Status
		s.mu.Unlock()
		s.log.Info("web: agent logged out with the portal", "agent", p.key(), "was", a.Status)
	})
}

// agentSignedIn logs the extension's agent in when it is logged out: back
// to the status the portal's sign-out took from it, else Available.
func (s *Server) agentSignedIn(p *principal) {
	if p == nil || p.Kind != "extension" {
		return
	}
	key := strings.ToLower(p.key())
	s.mu.Lock()
	was, ok := s.signedOutAgents[key]
	delete(s.signedOutAgents, key)
	s.mu.Unlock()
	a := s.agentOf(p)
	if a == nil || a.Status != "Logged Out" {
		return
	}
	if !ok || was == "" || was == "On Break" {
		was = "Available" // a break does not survive signing out
	}
	if err := s.sw.SetAgentStatus(core.AgentStatusChange{Agent: a.UUID, Domain: p.Domain, Status: was, Source: "web sign-in"}); err == nil {
		s.log.Info("web: agent back with the portal", "agent", p.key(), "status", was)
	}
}
