package discover

import (
	"context"
	"testing"
)

type fakeBody struct {
	m map[string]*Body
}

func (f *fakeBody) Get(_ context.Context, url string) (*Body, error) {
	if b, ok := f.m[url]; ok {
		return b, nil
	}
	return &Body{Status: 404, ContentType: "text/plain", Data: []byte("nope")}, nil
}

func html(s string) *Body {
	return &Body{Status: 200, ContentType: "text/html; charset=utf-8", Data: []byte(s)}
}
func js(s string) *Body {
	return &Body{Status: 200, ContentType: "application/javascript", Data: []byte(s)}
}

func TestDiscoverAllSources(t *testing.T) {
	f := &fakeBody{m: map[string]*Body{
		"http://x/": html(`
			<a href="/about">about</a>
			<a href="/w/clock">clock</a>
			<a href="https://external.com/evil">ext</a>
			<link rel="stylesheet" href="/style.css">
			<script src="/app.js"></script>`),
		"http://x/about": html(`<a href="/contact">contact</a>`),
		"http://x/app.js": js(`
			fetch("/api/users");
			const u = "/w/clock/data";
			var img = "/logo.png";`),
		"http://x/robots.txt": {Status: 200, ContentType: "text/plain",
			Data: []byte("User-agent: *\nDisallow: /admin\nSitemap: http://x/sitemap.xml\n")},
		"http://x/sitemap.xml": {Status: 200, ContentType: "application/xml",
			Data: []byte(`<urlset><url><loc>http://x/products</loc></url></urlset>`)},
	}}

	eps, err := Discover(context.Background(), Options{
		BaseURL:   "http://x",
		Crawl:     true,
		MineJS:    true,
		UseRobots: true,
		MaxDepth:  3,
		MaxPages:  50,
	}, f)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]Source{}
	for _, e := range eps {
		got[e.Path] = e.Source
	}

	want := []string{"about", "w/clock", "contact", "app.js", "api/users", "w/clock/data", "admin", "products"}
	for _, p := range want {
		if _, ok := got[p]; !ok {
			t.Errorf("missing endpoint %q; got %v", p, got)
		}
	}
	// external host must be filtered out
	for p := range got {
		if p == "evil" {
			t.Errorf("external endpoint leaked: %q", p)
		}
	}
	// source labelling spot checks
	if got["api/users"] != SourceJS {
		t.Errorf("api/users source = %q, want js", got["api/users"])
	}
	if got["admin"] != SourceRobots {
		t.Errorf("admin source = %q, want robots", got["admin"])
	}
	if got["products"] != SourceSitemap {
		t.Errorf("products source = %q, want sitemap", got["products"])
	}
}

func TestExtractJS(t *testing.T) {
	out := extractJS([]byte(`fetch('/api/v1/login'); x="/_next/data/BUILD/index.json"; y="https://cdn.x/lib.js"; z="/";`))
	set := map[string]bool{}
	for _, v := range out {
		set[v] = true
	}
	if !set["/api/v1/login"] {
		t.Errorf("expected /api/v1/login, got %v", out)
	}
	if set["/"] {
		t.Errorf("bare slash should be skipped")
	}
}
