// Package discover finds endpoints of a target by crawling its pages, mining
// its JavaScript, and reading robots.txt / sitemap.xml. It is for authorized
// testing only. The discovered paths are fed back to the brute-force engine
// as seed roots.
package discover

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"time"
)

// Body is a fetched document.
type Body struct {
	Status      int
	ContentType string
	Data        []byte
	FinalURL    string // after redirects
}

// BodyDoer fetches a full response body. Discovery needs the body (to parse
// links), which the engine.Doer deliberately discards, so it has its own
// fetch interface. Implementations must honor ctx.
type BodyDoer interface {
	Get(ctx context.Context, url string) (*Body, error)
}

// HTTPBodyDoer is a net/http-backed BodyDoer.
type HTTPBodyDoer struct {
	client  *http.Client
	maxBody int64
	ua      string
}

// NewHTTPBodyDoer builds a BodyDoer. maxBody caps how many bytes are read per
// response (0 => 2 MiB).
func NewHTTPBodyDoer(timeout time.Duration, skipTLS bool, maxBody int64) *HTTPBodyDoer {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if maxBody <= 0 {
		maxBody = 2 * 1024 * 1024
	}
	return &HTTPBodyDoer{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: skipTLS}, //nolint:gosec
				ForceAttemptHTTP2: true,
				MaxIdleConns:      32,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
		},
		maxBody: maxBody,
		ua:      "kho-beng-nae/2.0",
	}
}

// Get implements BodyDoer.
func (d *HTTPBodyDoer) Get(ctx context.Context, url string) (*Body, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", d.ua)
	req.Header.Set("Accept", "*/*")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, d.maxBody))
	final := url
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return &Body{
		Status:      resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Data:        data,
		FinalURL:    final,
	}, nil
}
