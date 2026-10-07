package engine

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Protocol is the negotiated HTTP major version of a target.
type Protocol uint8

const (
	ProtocolUnknown Protocol = iota
	ProtocolHTTP1
	ProtocolHTTP2
)

func (p Protocol) String() string {
	switch p {
	case ProtocolHTTP1:
		return "HTTP/1.1"
	case ProtocolHTTP2:
		return "HTTP/2"
	default:
		return "unknown"
	}
}

// Response is the transport-agnostic result of a single probe.
type Response struct {
	StatusCode int
	Size       int64
	Location   string // value of the Location header, when present
	Duration   time.Duration
}

// Doer probes one absolute URL with a GET request and returns the response
// metadata. Implementations MUST NOT follow redirects (the caller inspects
// 3xx status codes itself) and MUST honor ctx cancellation.
//
// A non-nil error means a transport failure (DNS, connect, TLS, timeout,
// reset) — the request could not be completed. An HTTP status such as 500 is
// NOT an error; it is returned in Response with a nil error. This distinction
// is what lets the engine retry transport failures without retrying
// legitimate server responses.
type Doer interface {
	Do(ctx context.Context, rawURL string) (*Response, error)
}

// TransportConfig tunes a Doer.
type TransportConfig struct {
	Timeout       time.Duration // per-request timeout
	MaxConns      int           // connection pool size
	SkipTLSVerify bool          // accept self-signed / invalid certs (labs)
	ForceHTTP2    bool          // attempt HTTP/2 upgrade
	UserAgent     string
	MaxBodyBytes  int64 // cap on bytes drained per response (0 = 64 KiB)
}

func (c TransportConfig) withDefaults() TransportConfig {
	if c.Timeout <= 0 {
		c.Timeout = 8 * time.Second
	}
	if c.MaxConns < 1 {
		c.MaxConns = 64
	}
	if c.UserAgent == "" {
		c.UserAgent = "kho-beng-nae/2.0"
	}
	if c.MaxBodyBytes <= 0 {
		c.MaxBodyBytes = 64 * 1024
	}
	return c
}

// HTTPDoer is a Doer backed by the standard library net/http client.
type HTTPDoer struct {
	client       *http.Client
	userAgent    string
	maxBodyBytes int64
}

// NewHTTPDoer builds a net/http-backed Doer. It is safe for concurrent use.
func NewHTTPDoer(cfg TransportConfig) *HTTPDoer {
	cfg = cfg.withDefaults()

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.SkipTLSVerify, //nolint:gosec // opt-in for labs
		},
		MaxIdleConns:        cfg.MaxConns * 2,
		MaxIdleConnsPerHost: cfg.MaxConns,
		MaxConnsPerHost:     cfg.MaxConns,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   cfg.ForceHTTP2,
	}

	return &HTTPDoer{
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
			// Never follow redirects: a 301/302 is a discovery signal.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		userAgent:    cfg.UserAgent,
		maxBodyBytes: cfg.MaxBodyBytes,
	}
}

// Do implements Doer.
func (d *HTTPDoer) Do(ctx context.Context, rawURL string) (*Response, error) {
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", d.userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Drain a bounded amount so the connection can be reused, but never let a
	// huge response stall a worker. Prefer Content-Length when the server
	// reports it.
	var size int64
	if resp.ContentLength >= 0 {
		size = resp.ContentLength
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, d.maxBodyBytes))
	} else {
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, d.maxBodyBytes))
		size = n
	}

	return &Response{
		StatusCode: resp.StatusCode,
		Size:       size,
		Location:   resp.Header.Get("Location"),
		Duration:   time.Since(start),
	}, nil
}

// DetectProtocol issues a HEAD request and reports the negotiated HTTP major
// version. It is used once per target before scanning begins.
func DetectProtocol(ctx context.Context, baseURL string, cfg TransportConfig) (Protocol, error) {
	cfg = cfg.withDefaults()
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.SkipTLSVerify, //nolint:gosec
			},
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL, nil)
	if err != nil {
		return ProtocolUnknown, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ProtocolUnknown, err
	}
	defer resp.Body.Close()

	switch resp.ProtoMajor {
	case 2:
		return ProtocolHTTP2, nil
	case 1:
		return ProtocolHTTP1, nil
	default:
		return ProtocolUnknown, fmt.Errorf("unsupported protocol: %s", resp.Proto)
	}
}
