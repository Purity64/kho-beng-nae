package engine

import "strings"

// Category groups a probe result for the UI.
type Category string

const (
	CatSuccess   Category = "success"    // 2xx
	CatRedirect  Category = "redirect"   // 3xx
	CatAuth      Category = "auth"       // 401, 403 — exists but protected
	CatClientErr Category = "client_err" // other 4xx (usually not found)
	CatServerErr Category = "server_err" // 5xx
	CatOther     Category = "other"
)

// Categorize maps an HTTP status code to a UI category.
func Categorize(status int) Category {
	switch {
	case status >= 200 && status < 300:
		return CatSuccess
	case status >= 300 && status < 400:
		return CatRedirect
	case status == 401 || status == 403:
		return CatAuth
	case status >= 400 && status < 500:
		return CatClientErr
	case status >= 500 && status < 600:
		return CatServerErr
	default:
		return CatOther
	}
}

// FoundPolicy decides which results count as "found" (worth showing and,
// possibly, recursing into). It is configurable per session.
type FoundPolicy struct {
	// Statuses explicitly treated as found. When empty, DefaultFoundStatuses
	// is used.
	Statuses map[int]bool
}

// DefaultFoundStatuses: existing-but-interesting responses.
func DefaultFoundStatuses() map[int]bool {
	return map[int]bool{
		200: true, 201: true, 202: true, 204: true,
		301: true, 302: true, 307: true, 308: true,
		401: true, 403: true, 405: true,
	}
}

// IsFound reports whether a status is considered a hit under this policy.
func (p FoundPolicy) IsFound(status int) bool {
	m := p.Statuses
	if len(m) == 0 {
		m = DefaultFoundStatuses()
	}
	return m[status]
}

// LooksLikeDirectory reports whether a hit is a plausible directory to recurse
// into: a trailing-slash path, or a redirect whose Location adds a trailing
// slash (the classic Apache/nginx "add slash" behavior).
func LooksLikeDirectory(path string, resp *Response) bool {
	if strings.HasSuffix(path, "/") {
		return true
	}
	if resp == nil {
		return false
	}
	if Categorize(resp.StatusCode) == CatRedirect {
		loc := resp.Location
		// e.g. request /admin -> 301 Location: /admin/
		if strings.HasSuffix(loc, path+"/") || strings.HasSuffix(loc, "/") {
			return true
		}
	}
	return false
}
