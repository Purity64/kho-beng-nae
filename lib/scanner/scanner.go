package scanner

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
	"time"

	"Kho-beng-nae/wordlist"
)

type Scanner struct {
	BaseURL string
	Workers int
	Client  *http.Client

	Wordlist *wordlist.Wordlist
}

func New(
	baseURL string,
	workers int,
	wl *wordlist.Wordlist,
) *Scanner {

	baseURL = strings.TrimRight(baseURL, "/")

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},

		MaxIdleConns:        workers * 2,
		MaxIdleConnsPerHost: workers,
		MaxConnsPerHost:     workers,

		IdleConnTimeout: 30 * time.Second,

		ForceAttemptHTTP2: true,
	}

	return &Scanner{
		BaseURL:  baseURL,
		Workers:  workers,
		Wordlist: wl,

		Client: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second,
		},
	}
}

func (s *Scanner) Scan(
	ctx context.Context,
) <-chan Result {

	jobs := make(
		chan uint32,
		s.Workers*2,
	)

	results := make(
		chan Result,
		s.Workers*2,
	)

	var wg sync.WaitGroup

	for i := 0; i < s.Workers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			s.worker(
				ctx,
				jobs,
				results,
			)
		}()
	}

	go func() {
		defer close(jobs)

		for i := 0; i < s.Wordlist.Len(); i++ {

			select {
			case <-ctx.Done():
				return

			case jobs <- uint32(i):
			}
		}
	}()

	go func() {
		wg.Wait()

		close(results)
	}()

	return results
}