package main

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/valyala/fasthttp"

	"Kho-beng-nae/internal/engine"
)

func tlsConfig(skipVerify bool) *tls.Config {
	return &tls.Config{InsecureSkipVerify: skipVerify} //nolint:gosec // opt-in for labs
}

// pickDoer chooses the transport for a detected protocol: fasthttp for HTTP/1,
// net/http (HTTP/2 multiplexing) for HTTP/2 or unknown.
func pickDoer(proto engine.Protocol, cfg engine.TransportConfig) engine.Doer {
	if proto == engine.ProtocolHTTP1 {
		return newFastDoer(cfg)
	}
	cfg.ForceHTTP2 = true
	return engine.NewHTTPDoer(cfg)
}

// fastDoer is an engine.Doer backed by valyala/fasthttp. It is used for
// HTTP/1 targets, where fasthttp's object reuse and lower allocation make it
// noticeably faster than net/http at high request volume. For HTTP/2 targets
// net/http is used instead (it multiplexes many requests over one connection).
type fastDoer struct {
	client  *fasthttp.Client
	timeout time.Duration
	ua      string
}

func newFastDoer(cfg engine.TransportConfig) *fastDoer {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	conns := cfg.MaxConns
	if conns < 1 {
		conns = 64
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = "kho-beng-nae/2.0"
	}
	return &fastDoer{
		client: &fasthttp.Client{
			MaxConnsPerHost:               conns,
			ReadTimeout:                   timeout,
			WriteTimeout:                  timeout,
			MaxIdleConnDuration:           30 * time.Second,
			NoDefaultUserAgentHeader:      true,
			DisableHeaderNamesNormalizing: true,
			// Accept self-signed certs in labs when requested.
			TLSConfig: tlsConfig(cfg.SkipTLSVerify),
		},
		timeout: timeout,
		ua:      ua,
	}
}

func (d *fastDoer) Do(ctx context.Context, rawURL string) (*engine.Response, error) {
	// Respect ctx cancellation up front.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.SetUserAgent(d.ua)
	req.SetRequestURI(rawURL)
	// Do not auto-follow redirects: a 301/302 is a discovery signal.

	start := time.Now()
	err := d.client.DoTimeout(req, resp, d.timeout)
	if err != nil {
		return nil, err
	}

	loc := ""
	if b := resp.Header.Peek("Location"); len(b) > 0 {
		loc = string(b)
	}
	return &engine.Response{
		StatusCode: resp.StatusCode(),
		Size:       int64(len(resp.Body())),
		Location:   loc,
		Duration:   time.Since(start),
	}, nil
}
