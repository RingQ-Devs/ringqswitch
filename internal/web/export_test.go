package web

import "time"

// SetAgentLogoutDelay shortens the wait before an agent that signed out of
// the portal is logged out of its queues (tests).
func SetAgentLogoutDelay(d time.Duration) { agentLogoutDelay = d }

// WakeupNow places the wake-up calls that are due (tests; the server does
// it every 15 seconds).
func (s *Server) WakeupNow() { s.wakeupDue() }

// SetSSODiscovery points a single sign-on provider at a test identity provider.
func SetSSODiscovery(provider, url string) { ssoDiscoveryOverride[provider] = url }
