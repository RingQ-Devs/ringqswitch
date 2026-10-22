package core

import (
	"time"
)

// CacheReport says what ClearCache reloaded.
type CacheReport struct {
	Domains    int   `json:"domains"`
	Extensions int   `json:"extensions"`
	Gateways   int   `json:"gateways"`
	Dialplans  int   `json:"dialplans"`
	TookMS     int64 `json:"took_ms"`
}

// ClearCache forgets what RingQswitch keeps between database reads (the
// carriers' resolved addresses, compiled number patterns) and reloads the
// configuration from the database at once, like
// "xml_flush_cache" and "reloadxml" together. Calls in progress continue.
func (c *Core) ClearCache() (CacheReport, error) {
	start := time.Now()
	regexCache.Range(func(k, _ any) bool { regexCache.Delete(k); return true })
	c.mu.Lock()
	for _, g := range c.gateways {
		g.mu.Lock()
		g.addrs = map[string]cachedAddr{}
		g.mu.Unlock()
	}
	c.mu.Unlock()
	if err := c.Reload(); err != nil {
		return CacheReport{}, err
	}
	cfg := c.config()
	rep := CacheReport{Gateways: len(cfg.Gateways)}
	if cfg.Directory != nil {
		rep.Domains = len(cfg.Directory.Domains)
		for _, d := range cfg.Directory.Domains {
			rep.Extensions += len(d.Users)
		}
	}
	if cfg.Dialplan != nil {
		for _, ctx := range cfg.Dialplan.Contexts {
			rep.Dialplans += len(ctx.Extensions)
		}
	}
	rep.TookMS = time.Since(start).Milliseconds()
	c.recordEvent("system", "cache cleared")
	return rep, nil
}
