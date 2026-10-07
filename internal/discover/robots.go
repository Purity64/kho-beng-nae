package discover

import (
	"context"
	"regexp"
	"strings"
)

var reSitemapLoc = regexp.MustCompile(`(?i)<loc>\s*([^<\s]+)\s*</loc>`)

// fetchRobots reads /robots.txt and returns the paths it mentions (Allow,
// Disallow, Sitemap) plus any sitemap URLs found.
func fetchRobots(ctx context.Context, bd BodyDoer, origin string) (paths []string, sitemaps []string) {
	b, err := bd.Get(ctx, origin+"/robots.txt")
	if err != nil || b.Status >= 400 || len(b.Data) == 0 {
		return nil, nil
	}
	for _, line := range strings.Split(string(b.Data), "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "allow:"):
			paths = append(paths, strings.TrimSpace(line[len("allow:"):]))
		case strings.HasPrefix(lower, "disallow:"):
			paths = append(paths, strings.TrimSpace(line[len("disallow:"):]))
		case strings.HasPrefix(lower, "sitemap:"):
			sitemaps = append(sitemaps, strings.TrimSpace(line[len("sitemap:"):]))
		}
	}
	return paths, sitemaps
}

// fetchSitemap reads a sitemap.xml (or sitemap index) and returns the <loc>
// URLs. Nested sitemap indexes are followed one level.
func fetchSitemap(ctx context.Context, bd BodyDoer, url string, depth int) []string {
	b, err := bd.Get(ctx, url)
	if err != nil || b.Status >= 400 || len(b.Data) == 0 {
		return nil
	}
	var out []string
	for _, m := range reSitemapLoc.FindAllStringSubmatch(string(b.Data), -1) {
		loc := strings.TrimSpace(m[1])
		if loc == "" {
			continue
		}
		// A sitemap index points at more sitemaps; follow once.
		if depth < 1 && strings.Contains(strings.ToLower(loc), "sitemap") && strings.HasSuffix(strings.ToLower(loc), ".xml") {
			out = append(out, fetchSitemap(ctx, bd, loc, depth+1)...)
			continue
		}
		out = append(out, loc)
	}
	return out
}
