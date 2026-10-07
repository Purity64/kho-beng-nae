package discover

import (
	"regexp"
	"strings"
)

var (
	// HTML attribute values that carry URLs.
	reAttr = regexp.MustCompile(`(?i)(?:href|src|action|data-src|data-url)\s*=\s*["']([^"'<>]+)["']`)
	// srcset entries and CSS url(...).
	reCSSURL = regexp.MustCompile(`url\(\s*["']?([^"')]+)["']?\s*\)`)
	// Quoted path- or URL-like strings inside JS: "/api/x", "https://h/x".
	reJSPath = regexp.MustCompile(`["'` + "`" + `](\/[a-zA-Z0-9_\-./~%]{1,200}|https?:\/\/[a-zA-Z0-9_\-./~%:?=&]{3,300})["'` + "`" + `]`)
	// Next.js data / build manifest references.
	reNextData = regexp.MustCompile(`\/_next\/data\/[^"'` + "`" + ` )]+`)
)

// extractHTML pulls candidate link targets out of an HTML document.
func extractHTML(body []byte) []string {
	s := string(body)
	set := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || strings.HasPrefix(v, "#") ||
			strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "mailto:") ||
			strings.HasPrefix(v, "tel:") || strings.HasPrefix(v, "data:") {
			return
		}
		set[v] = true
	}
	for _, m := range reAttr.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	for _, m := range reCSSURL.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	return keysOf(set)
}

// extractJS pulls candidate endpoints out of a JavaScript file.
func extractJS(body []byte) []string {
	s := string(body)
	set := map[string]bool{}
	for _, m := range reJSPath.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		// Skip obvious non-endpoints.
		if v == "/" || strings.Contains(v, " ") {
			continue
		}
		set[v] = true
	}
	for _, m := range reNextData.FindAllString(s, -1) {
		set[m] = true
	}
	return keysOf(set)
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// isJS reports whether a URL/content-type looks like JavaScript.
func isJS(url, contentType string) bool {
	if strings.Contains(contentType, "javascript") {
		return true
	}
	u := strings.ToLower(url)
	// strip query
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	return strings.HasSuffix(u, ".js") || strings.HasSuffix(u, ".mjs")
}

// isHTML reports whether a response is HTML worth crawling for more links.
func isHTML(contentType string) bool {
	return strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml")
}
