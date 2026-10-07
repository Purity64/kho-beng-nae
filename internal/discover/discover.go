package discover

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Source labels where an endpoint was found.
type Source string

const (
	SourceCrawl   Source = "crawl"
	SourceJS      Source = "js"
	SourceRobots  Source = "robots"
	SourceSitemap Source = "sitemap"
)

// Endpoint is a discovered path on the target.
type Endpoint struct {
	Path   string `json:"path"`   // relative to origin, no leading slash
	URL    string `json:"url"`    // absolute URL
	Source Source `json:"source"` // how it was found
}

// Options configures a discovery run.
type Options struct {
	BaseURL   string
	MaxPages  int  // cap on HTML pages crawled
	MaxDepth  int  // crawl link depth
	Crawl     bool // follow HTML links
	MineJS    bool // fetch and parse .js files
	UseRobots bool // read robots.txt + sitemap.xml
	Timeout   int  // seconds (for the default BodyDoer)
	SkipTLS   bool
	OnFound   func(Endpoint) // optional live callback
}

func (o Options) withDefaults() Options {
	if o.MaxPages <= 0 {
		o.MaxPages = 200
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = 3
	}
	return o
}

// Discover runs the configured discovery sources and returns the deduplicated
// set of endpoints (sorted by path). bd may be nil, in which case a default
// net/http BodyDoer is built from the options.
func Discover(ctx context.Context, opts Options, bd BodyDoer) ([]Endpoint, error) {
	opts = opts.withDefaults()

	base, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	origin := base.Scheme + "://" + base.Host

	if bd == nil {
		bd = NewHTTPBodyDoer(secs(opts.Timeout), opts.SkipTLS, 0)
	}

	d := &crawler{
		opts:    opts,
		base:    base,
		origin:  origin,
		bd:      bd,
		found:   map[string]Endpoint{},
		visited: map[string]bool{},
	}

	// robots.txt + sitemap.xml
	if opts.UseRobots {
		paths, sitemaps := fetchRobots(ctx, bd, origin)
		for _, p := range paths {
			d.record(p, base, SourceRobots)
		}
		for _, sm := range sitemaps {
			for _, loc := range fetchSitemap(ctx, bd, sm, 0) {
				d.record(loc, base, SourceSitemap)
			}
		}
		// Try the conventional sitemap location too.
		for _, loc := range fetchSitemap(ctx, bd, origin+"/sitemap.xml", 0) {
			d.record(loc, base, SourceSitemap)
		}
	}

	// crawl + JS mining
	if opts.Crawl || opts.MineJS {
		d.run(ctx)
	}

	out := make([]Endpoint, 0, len(d.found))
	for _, e := range d.found {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

type crawler struct {
	opts    Options
	base    *url.URL
	origin  string
	bd      BodyDoer
	found   map[string]Endpoint
	visited map[string]bool
}

type crawlItem struct {
	url   string
	depth int
}

func (c *crawler) run(ctx context.Context) {
	queue := []crawlItem{{url: c.origin + c.base.Path, depth: 0}}
	if c.base.Path == "" {
		queue[0].url = c.origin + "/"
	}
	pages := 0

	for len(queue) > 0 {
		if ctx.Err() != nil {
			return
		}
		item := queue[0]
		queue = queue[1:]

		norm := stripFragment(item.url)
		if c.visited[norm] {
			continue
		}
		c.visited[norm] = true
		if pages >= c.opts.MaxPages {
			return
		}
		pages++

		b, err := c.bd.Get(ctx, item.url)
		if err != nil || b == nil {
			continue
		}

		switch {
		case c.opts.MineJS && isJS(item.url, b.ContentType):
			for _, raw := range extractJS(b.Data) {
				c.record(raw, refBase(item.url, c.base), SourceJS)
			}
		case isHTML(b.ContentType):
			for _, raw := range extractHTML(b.Data) {
				abs := c.resolve(raw, c.base)
				if abs == nil {
					continue
				}
				c.record(raw, c.base, SourceCrawl)
				// enqueue same-origin HTML for deeper crawl
				if c.opts.Crawl && item.depth < c.opts.MaxDepth && c.sameOrigin(abs) && looksHTML(abs.Path) {
					queue = append(queue, crawlItem{url: abs.String(), depth: item.depth + 1})
				}
				// fetch same-origin JS immediately
				if c.opts.MineJS && c.sameOrigin(abs) && isJS(abs.Path, "") && !c.visited[stripFragment(abs.String())] {
					queue = append(queue, crawlItem{url: abs.String(), depth: item.depth})
				}
			}
		}
	}
}

// record resolves raw against ref, filters to the target origin, and stores
// the endpoint (deduped by path).
func (c *crawler) record(raw string, ref *url.URL, src Source) {
	abs := c.resolve(raw, ref)
	if abs == nil || !c.sameOrigin(abs) {
		return
	}
	path := strings.TrimLeft(abs.Path, "/")
	if path == "" {
		return
	}
	if _, exists := c.found[path]; exists {
		return
	}
	e := Endpoint{Path: path, URL: stripFragment(abs.String()), Source: src}
	c.found[path] = e
	if c.opts.OnFound != nil {
		c.opts.OnFound(e)
	}
}

func (c *crawler) resolve(raw string, ref *url.URL) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	if ref == nil {
		ref = c.base
	}
	return ref.ResolveReference(u)
}

func (c *crawler) sameOrigin(u *url.URL) bool {
	return strings.EqualFold(u.Host, c.base.Host) && (u.Scheme == "http" || u.Scheme == "https")
}

// ---- small helpers --------------------------------------------------------

func stripFragment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}

// refBase returns the URL to resolve a JS file's relative references against.
func refBase(jsURL string, fallback *url.URL) *url.URL {
	u, err := url.Parse(jsURL)
	if err != nil {
		return fallback
	}
	return u
}

// looksHTML guesses whether a path is a navigable page (worth crawling) rather
// than a static asset.
func looksHTML(path string) bool {
	p := strings.ToLower(path)
	for _, ext := range []string{".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".mp4", ".webp", ".pdf", ".zip"} {
		if strings.HasSuffix(p, ext) {
			return false
		}
	}
	return true
}

func secs(n int) time.Duration {
	if n <= 0 {
		n = 10
	}
	return time.Duration(n) * time.Second
}
