package web

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// What an AI agent knows besides its prompt: the web pages it was given
// (fetched with the pages they link to on the same site) and its FAQ text,
// cut into chunks. A small amount goes into the agent's instructions as a
// whole; more is searched (BM25) when the caller asks something.

const (
	aiCrawlPages    = 40      // pages per agent
	aiCrawlDepth    = 2       // links followed from a given page
	aiCrawlPageMax  = 3 << 20 // bytes read per page
	aiChunkSize     = 900     // characters per chunk
	aiChunksMax     = 600
	aiCrawlAgent    = "RingQswitch-AIAgent/1.0 (+knowledge fetch for a voice assistant)"
	aiInlineWeb     = 10000 // web text up to this goes into the instructions
	aiInlineFAQ     = 14000
	aiSearchResults = 3
)

type aiCrawlState struct {
	Running  bool      `json:"running"`
	Pages    int       `json:"pages"`
	Chunks   int       `json:"chunks"`
	Error    string    `json:"error,omitempty"`
	Finished time.Time `json:"finished"`
	again    []string  // fetch again with these addresses when done
	rerun    bool
}

type aiCrawls struct {
	mu sync.Mutex
	m  map[string]*aiCrawlState
}

// aiKnowledgeKick (re)fetches an agent's web pages in the background.
func (s *Server) aiKnowledgeKick(domainUUID, agentUUID string, urls []string) {
	s.aiCrawl.mu.Lock()
	if s.aiCrawl.m == nil {
		s.aiCrawl.m = map[string]*aiCrawlState{}
	}
	st := s.aiCrawl.m[agentUUID]
	if st != nil && st.Running {
		st.again, st.rerun = urls, true
		s.aiCrawl.mu.Unlock()
		return
	}
	st = &aiCrawlState{Running: true}
	s.aiCrawl.m[agentUUID] = st
	s.aiCrawl.mu.Unlock()
	go func() {
		for {
			pages, chunks, err := s.aiFetchKnowledge(domainUUID, agentUUID, urls)
			s.aiCrawl.mu.Lock()
			st.Pages, st.Chunks, st.Finished, st.Error = pages, chunks, time.Now(), ""
			if err != nil {
				st.Error = err.Error()
				s.log.Warn("ai agent: fetching web pages", "agent", agentUUID, "err", err)
			}
			if !st.rerun {
				st.Running = false
				s.aiCrawl.mu.Unlock()
				return
			}
			urls, st.rerun = st.again, false
			s.aiCrawl.mu.Unlock()
		}
	}()
}

func (s *Server) aiCrawlStatus(agentUUID string) aiCrawlState {
	s.aiCrawl.mu.Lock()
	defer s.aiCrawl.mu.Unlock()
	if st := s.aiCrawl.m[agentUUID]; st != nil {
		return *st
	}
	return aiCrawlState{}
}

type aiPage struct {
	url, title string
	lines      []string
}

// aiFetchKnowledge fetches the pages and replaces the agent's web chunks.
func (s *Server) aiFetchKnowledge(domainUUID, agentUUID string, urls []string) (int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var pages []aiPage
	var ferr error
	if len(urls) > 0 {
		pages, ferr = aiCrawlSites(ctx, s.aiHTTP(), urls)
	}
	chunks := aiChunkPages(pages)
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `delete from rq_ai_knowledge where ai_agent_uuid = $1::uuid`, agentUUID); err != nil {
		return 0, 0, err
	}
	now := time.Now()
	for i, c := range chunks {
		if _, err := tx.Exec(ctx, `insert into rq_ai_knowledge (ai_knowledge_uuid, ai_agent_uuid, domain_uuid, source, title, chunk_no,
			content, fetched_at) values ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8)`, newUUID(), agentUUID, domainUUID, c.source,
			c.title, i, c.text, now); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	if len(pages) == 0 && ferr == nil && len(urls) > 0 {
		ferr = errors.New("no page could be read")
	}
	return len(pages), len(chunks), ferr
}

func (s *Server) aiHTTP() *http.Client {
	if s.aiClient != nil {
		return s.aiClient
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 15 * time.Second}}
}

// aiCrawlSites reads the pages and the same-site pages they link to.
func aiCrawlSites(ctx context.Context, client *http.Client, starts []string) ([]aiPage, error) {
	type item struct {
		u     *url.URL
		depth int
	}
	var queue []item
	seen := map[string]bool{}
	hosts := map[string]bool{}
	norm := func(u *url.URL) string {
		v := *u
		v.Fragment = ""
		v.Host = strings.ToLower(v.Host)
		v.Path = strings.TrimSuffix(v.Path, "/")
		return v.String()
	}
	for _, raw := range starts {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		hosts[strings.ToLower(u.Host)] = true
		if k := norm(u); !seen[k] {
			seen[k] = true
			queue = append(queue, item{u, 0})
		}
	}
	robots := map[string][]string{}
	var pages []aiPage
	var lastErr error
	for len(queue) > 0 && len(pages) < aiCrawlPages {
		if ctx.Err() != nil {
			break
		}
		it := queue[0]
		queue = queue[1:]
		host := strings.ToLower(it.u.Host)
		dis, ok := robots[host]
		if !ok {
			dis = aiRobots(ctx, client, it.u)
			robots[host] = dis
		}
		if aiDisallowed(dis, it.u.Path) {
			continue
		}
		pg, links, err := aiFetchPage(ctx, client, it.u)
		if err != nil {
			lastErr = err
			continue
		}
		if len(pg.lines) > 0 {
			pages = append(pages, pg)
		}
		if it.depth >= aiCrawlDepth {
			continue
		}
		for _, l := range links {
			if !hosts[strings.ToLower(l.Host)] || aiSkipLink(l) {
				continue
			}
			if k := norm(l); !seen[k] {
				seen[k] = true
				queue = append(queue, item{l, it.depth + 1})
			}
		}
	}
	if len(pages) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return pages, nil
}

// aiSkipLink leaves out files that are not pages.
func aiSkipLink(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return true
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".pdf", ".jpg", ".jpeg", ".png", ".gif", ".svg", ".webp", ".ico", ".css", ".js", ".json", ".xml", ".zip", ".gz", ".mp3",
		".mp4", ".wav", ".avi", ".mov", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".exe", ".dmg", ".apk", ".woff", ".woff2", ".ttf":
		return true
	}
	low := strings.ToLower(u.Path)
	for _, p := range []string{"/wp-admin", "/wp-login", "/login", "/signin", "/sign-in", "/cart", "/checkout", "/account", "/feed"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// aiRobots reads the Disallow rules for all robots ("User-agent: *").
func aiRobots(ctx context.Context, client *http.Client, u *url.URL) []string {
	ru := url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ru.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", aiCrawlAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var out []string
	all := false
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 512<<10))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "user-agent":
			all = v == "*"
		case "disallow":
			if all && v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func aiDisallowed(rules []string, p string) bool {
	if p == "" {
		p = "/"
	}
	for _, r := range rules {
		if strings.HasPrefix(p, strings.TrimSuffix(r, "*")) {
			return true
		}
	}
	return false
}

func aiFetchPage(ctx context.Context, client *http.Client, u *url.URL) (aiPage, []*url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return aiPage{}, nil, err
	}
	req.Header.Set("User-Agent", aiCrawlAgent)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return aiPage{}, nil, fmt.Errorf("%s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return aiPage{}, nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	body := io.LimitReader(resp.Body, aiCrawlPageMax)
	final := resp.Request.URL
	switch {
	case strings.Contains(ct, "text/plain"):
		b, err := io.ReadAll(body)
		if err != nil {
			return aiPage{}, nil, err
		}
		return aiPage{url: final.String(), title: path.Base(final.Path), lines: aiLines(string(b))}, nil, nil
	case ct == "" || strings.Contains(ct, "html"):
		pg, links := aiParseHTML(body, final)
		return pg, links, nil
	}
	return aiPage{}, nil, fmt.Errorf("%s: not a web page (%s)", u, ct)
}

// aiParseHTML takes the readable text and links of a page.
func aiParseHTML(r io.Reader, base *url.URL) (aiPage, []*url.URL) {
	z := html.NewTokenizer(r)
	var b strings.Builder
	var links []*url.URL
	var title, desc string
	skip := 0 // inside script, style...
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			pg := aiPage{url: base.String(), title: strings.TrimSpace(title)}
			if desc != "" {
				pg.lines = append(pg.lines, desc)
			}
			pg.lines = append(pg.lines, aiLines(b.String())...)
			if pg.title == "" {
				pg.title = base.Host + base.Path
			}
			return pg, links
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			switch tag {
			case "script", "style", "noscript", "svg", "template", "iframe", "canvas", "select":
				if tt == html.StartTagToken {
					skip++
				} else if tt == html.EndTagToken && skip > 0 {
					skip--
				}
				continue
			case "title":
				inTitle = tt == html.StartTagToken
				continue
			}
			if tt != html.EndTagToken && hasAttr {
				var key, val []byte
				more := true
				attrs := map[string]string{}
				for more {
					key, val, more = z.TagAttr()
					attrs[string(key)] = string(val)
				}
				switch tag {
				case "a":
					if h := strings.TrimSpace(attrs["href"]); h != "" && !strings.HasPrefix(h, "#") && !strings.HasPrefix(strings.ToLower(h), "javascript:") {
						if l, err := base.Parse(h); err == nil {
							links = append(links, l)
						}
					}
				case "meta":
					if n := strings.ToLower(attrs["name"]); n == "description" || strings.ToLower(attrs["property"]) == "og:description" {
						if desc == "" {
							desc = strings.TrimSpace(attrs["content"])
						}
					}
				case "img":
					if alt := strings.TrimSpace(attrs["alt"]); alt != "" && skip == 0 && len(alt) > 20 {
						b.WriteString(" " + alt + " ")
					}
				}
			}
			switch tag {
			case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article", "header", "footer",
				"ul", "ol", "table", "blockquote", "dd", "dt", "form", "main", "aside", "nav", "address", "hr", "pre":
				b.WriteString("\n")
			case "td", "th":
				if tt == html.StartTagToken {
					b.WriteString(" | ")
				}
			}
		case html.TextToken:
			if inTitle {
				title += string(z.Text())
				continue
			}
			if skip == 0 {
				b.Write(z.Text())
			}
		}
	}
}

// aiLines splits text into its non-empty lines, spaces collapsed.
func aiLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.Join(strings.Fields(l), " ")
		l = strings.Trim(l, "| ")
		if len([]rune(l)) >= 2 {
			out = append(out, l)
		}
	}
	return out
}

type aiChunk struct {
	source, title, text string
}

// aiChunkPages cuts the pages into chunks; lines every page repeats (menus,
// footers) are kept once.
func aiChunkPages(pages []aiPage) []aiChunk {
	count := map[string]int{}
	for _, p := range pages {
		seen := map[string]bool{}
		for _, l := range p.lines {
			if !seen[l] {
				seen[l] = true
				count[l]++
			}
		}
	}
	common := func(l string) bool {
		return count[l] >= 3 && float64(count[l]) >= 0.4*float64(len(pages))
	}
	kept := map[string]bool{}
	var out []aiChunk
	for _, p := range pages {
		var lines []string
		for _, l := range p.lines {
			if common(l) {
				if kept[l] {
					continue
				}
				kept[l] = true
			}
			lines = append(lines, l)
		}
		for _, t := range aiChunkText(lines) {
			out = append(out, aiChunk{source: p.url, title: p.title, text: t})
			if len(out) >= aiChunksMax {
				return out
			}
		}
	}
	return out
}

// aiChunkText joins lines into chunks of about aiChunkSize characters.
func aiChunkText(lines []string) []string {
	var out []string
	var b strings.Builder
	for _, l := range lines {
		for len(l) > aiChunkSize*2 { // a very long line: cut at a space
			cut := strings.LastIndex(l[:aiChunkSize], " ")
			if cut <= 0 {
				cut = aiChunkSize
			}
			out = append(out, strings.TrimSpace(l[:cut]))
			l = strings.TrimSpace(l[cut:])
		}
		if b.Len() > 0 && b.Len()+len(l) > aiChunkSize {
			out = append(out, b.String())
			b.Reset()
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(l)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// aiFAQChunks cuts the FAQ text at blank lines (a question and its answer
// stay together).
func aiFAQChunks(text string) []string {
	var out []string
	for _, block := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if block = strings.TrimSpace(block); block != "" {
			out = append(out, aiChunkText(strings.Split(block, "\n"))...)
		}
	}
	return out
}

// ---- search

type aiIndex struct {
	docs  []aiChunk
	terms []map[string]int
	lens  []int
	df    map[string]int
	avg   float64
}

func newAIIndex(docs []aiChunk) *aiIndex {
	x := &aiIndex{docs: docs, df: map[string]int{}}
	total := 0
	for _, d := range docs {
		tf := map[string]int{}
		toks := aiTokens(d.title + " " + d.text)
		for _, t := range toks {
			tf[t]++
		}
		for t := range tf {
			x.df[t]++
		}
		x.terms = append(x.terms, tf)
		x.lens = append(x.lens, len(toks))
		total += len(toks)
	}
	if len(docs) > 0 {
		x.avg = float64(total) / float64(len(docs))
	}
	return x
}

// search returns the best chunks for a question (BM25).
func (x *aiIndex) search(q string, n int) []aiChunk {
	if x == nil || len(x.docs) == 0 {
		return nil
	}
	const k1, b = 1.2, 0.75
	type hit struct {
		i     int
		score float64
	}
	var hits []hit
	qt := aiTokens(q)
	N := float64(len(x.docs))
	for i, tf := range x.terms {
		score := 0.0
		for _, t := range qt {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (N-float64(x.df[t])+0.5)/(float64(x.df[t])+0.5))
			score += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(x.lens[i])/math.Max(x.avg, 1)))
		}
		if score > 0 {
			hits = append(hits, hit{i, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	var out []aiChunk
	for _, h := range hits {
		if len(out) == n {
			break
		}
		out = append(out, x.docs[h.i])
	}
	return out
}

var aiStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by can do does for from has have how i if in is it its me my of on or
		our so that the their them then there these they this to us was we what when where which who why will with you your
		about any all also am been being did get got just like more most much no not now off out over please some such than
		tell want would could should shall may might know need`) {
		aiStop[w] = true
	}
}

// aiTokens lowercases and splits text into words (CJK in pairs of
// characters), leaving out common words.
func aiTokens(s string) []string {
	var out []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 1 {
			w := string(word)
			if !aiStop[w] {
				out = append(out, aiStem(w))
			}
		}
		word = word[:0]
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			out = append(out, string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r):
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return out
}

// aiStem folds simple English plurals and endings ("prices" = "price").
func aiStem(w string) string {
	if len(w) <= 4 {
		return w
	}
	for _, suf := range []string{"ies", "ing", "es", "ed", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			base := strings.TrimSuffix(w, suf)
			if suf == "ies" {
				return base + "y"
			}
			if suf == "es" && !strings.HasSuffix(base, "s") && !strings.HasSuffix(base, "x") && !strings.HasSuffix(base, "ch") && !strings.HasSuffix(base, "sh") {
				return base + "e"
			}
			return base
		}
	}
	return w
}

// ---- API

func (s *Server) aiKnowledgeAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, dom domainRow, agent string) error {
	if !reUUID.MatchString(agent) {
		return notFound("AI agent")
	}
	var urls string
	if err := s.store.Pool().QueryRow(ctx, `select coalesce(knowledge_urls, '') from rq_ai_agents where domain_uuid = $1::uuid
		and ai_agent_uuid = $2::uuid`, dom.uuid, agent).Scan(&urls); err != nil {
		return notFound("AI agent")
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := s.store.JSONRows(ctx, `select jsonb_build_object('source', source, 'title', coalesce(max(title), ''),
			'chunks', count(*), 'chars', sum(length(content)), 'fetched_at', max(fetched_at))
			from rq_ai_knowledge where ai_agent_uuid = $1::uuid group by source order by min(chunk_no)`, agent)
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []map[string]string{}
		}
		writeJSON(w, map[string]any{"pages": rows, "status": s.aiCrawlStatus(agent)})
		return nil
	case http.MethodPost:
		s.aiKnowledgeKick(dom.uuid, agent, strings.Fields(urls))
		writeJSON(w, map[string]any{"status": s.aiCrawlStatus(agent)})
		return nil
	}
	return errMethod
}
