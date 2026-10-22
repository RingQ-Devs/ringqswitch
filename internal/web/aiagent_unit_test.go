package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAICrawlSite(t *testing.T) {
	var hits []string
	mux := http.NewServeMux()
	page := func(title, body string) string {
		return `<html><head><title>` + title + `</title><meta name="description" content="` + title + ` page">
			<style>.x{color:red}</style><script>var secret = "do not index";</script></head><body>
			<nav><a href="/">Home</a> <a href="/pricing">Pricing</a> <a href="/support/">Support</a></nav>` + body +
			`<footer>Call us on +65 6000 0000. RingQ Pte Ltd.</footer></body></html>`
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/robots.txt":
			fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
		case "/":
			fmt.Fprint(w, page("RingQ", `<h1>Cloud contact centre</h1><p>RingQ runs your calls, WhatsApp and chats in one place.</p>
				<a href="/private/admin">Admin</a> <a href="https://elsewhere.example/">Elsewhere</a> <a href="/brochure.pdf">PDF</a>
				<a href="javascript:void(0)">x</a>`))
		case "/pricing":
			fmt.Fprint(w, page("Pricing", `<table><tr><td>Starter plan</td><td>$19 per user per month</td></tr></table>
				<a href="/pricing/details">More</a>`))
		case "/pricing/details":
			fmt.Fprint(w, page("Details", `<p>Annual billing saves 15 percent.</p><a href="/deeper">Deeper</a>`))
		case "/support", "/support/":
			fmt.Fprint(w, page("Support", `<p>Support is open 24 hours a day by phone and email.</p>`))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	pages, err := aiCrawlSites(context.Background(), srv.Client(), []string{srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	var all []string
	for _, p := range pages {
		titles[p.title] = true
		all = append(all, strings.Join(p.lines, "\n"))
	}
	for _, want := range []string{"RingQ", "Pricing", "Details", "Support"} {
		if !titles[want] {
			t.Errorf("page %q not read (pages %v)", want, titles)
		}
	}
	text := strings.Join(all, "\n")
	for _, bad := range []string{"do not index", "color:red"} {
		if strings.Contains(text, bad) {
			t.Errorf("page text holds %q", bad)
		}
	}
	if !strings.Contains(text, "Starter plan | $19 per user per month") || !strings.Contains(text, "RingQ page") {
		t.Errorf("page text: %s", text)
	}
	for _, h := range hits {
		if strings.HasPrefix(h, "/private") || h == "/brochure.pdf" || h == "/deeper" {
			t.Errorf("fetched %s (robots.txt, not a page, or too deep)", h)
		}
	}

	// The footer every page repeats is kept once.
	chunks := aiChunkPages(pages)
	n := 0
	for _, c := range chunks {
		n += strings.Count(c.text, "Call us on +65 6000 0000")
	}
	if n != 1 {
		t.Errorf("the footer is in %d chunks", n)
	}
	idx := newAIIndex(chunks)
	if got := idx.search("How much is the starter plan price?", 1); len(got) != 1 || got[0].title != "Pricing" {
		t.Errorf("search found %+v", got)
	}
	if got := idx.search("is support open at night", 1); len(got) != 1 || got[0].title != "Support" {
		t.Errorf("search found %+v", got)
	}
}

func TestAITextHelpers(t *testing.T) {
	if g := aiGreetings(`["Hi there!", "  ", "Hello"]`); len(g) != 2 || g[1] != "Hello" {
		t.Errorf("greetings %q", g)
	}
	if g := aiGreetings("Hi\n\nWelcome"); len(g) != 2 {
		t.Errorf("greetings from lines %q", g)
	}
	urls, err := aiParseURLs("ringq.io/pricing  https://ringq.io/#top\nhttps://ringq.io/")
	if err != nil || len(urls) != 2 || urls[0] != "https://ringq.io/pricing" {
		t.Errorf("urls %q %v", urls, err)
	}
	if _, err := aiParseURLs("ftp://x.example"); err == nil {
		t.Error("ftp address accepted")
	}
	if toks := aiTokens("Prices of the Plans 价格表"); strings.Join(toks, ",") != "price,plan,价格,格表" {
		t.Errorf("tokens %q", toks)
	}
	faq := aiFAQChunks("Q: Hours?\nA: 9 to 6.\n\nQ: Where?\nA: Singapore.")
	if len(faq) != 2 || !strings.Contains(faq[1], "Singapore") {
		t.Errorf("faq chunks %q", faq)
	}
	a := &aiAgent{Name: "Ava", Languages: []string{"en", "ms"}, Prompt: "Sell RingQ.", AllowEnd: true,
		Keywords: []aiKeyword{{Word: "agent", Dest: "transfer:1000 XML x"}}}
	in := aiInstructions(a, "Office hours 9 to 6.", true)
	for _, want := range []string{"You are Ava", "English, Malay", "Sell RingQ.", "Office hours 9 to 6.", "search_knowledge",
		"transfer_call with it: agent", "end_call"} {
		if !strings.Contains(in, want) {
			t.Errorf("instructions lack %q:\n%s", want, in)
		}
	}
	tools := aiTools(a, false)
	if len(tools) != 2 || tools[0].Name != "transfer_call" || tools[1].Name != "end_call" {
		t.Errorf("tools %+v", tools)
	}
	u, _ := url.Parse("https://x.example/a/b.html")
	pg, links := aiParseHTML(strings.NewReader(`<title>T</title><p>Hello <b>world</b></p><a href="../c">c</a>`), u)
	if pg.title != "T" || strings.Join(pg.lines, "|") != "Hello world" || len(links) != 1 || links[0].String() != "https://x.example/c" {
		t.Errorf("page %+v links %v", pg, links)
	}
	if w := wavPCM16([]byte{1, 2, 3}, 24000); len(w) != 46 || string(w[:4]) != "RIFF" {
		t.Errorf("wav %d bytes", len(w))
	}
}
