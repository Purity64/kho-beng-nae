package scanner

import (
	"Kho-beng-nae/wordlist"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
)

type ScannerFast struct {
	BaseURL string
	Workers int

	Wordlist *wordlist.Wordlist
	FastClient *fasthttp.Client
}

func NewFastHTTP(
	baseURL string,
	workers int,
	wl *wordlist.Wordlist,
) *ScannerFast {
	if workers < 1 {
		workers = 1
	}
	baseURL = strings.TrimRight(baseURL, "/")

	client := &fasthttp.Client{
		MaxConnsPerHost: workers,
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    5 * time.Second,
	}

	return &ScannerFast{
		BaseURL:    baseURL,
		Workers:    workers,
		Wordlist:   wl,
		FastClient: client,
	}
}

func (s *ScannerFast) Scan(
	ctx context.Context,
)<-chan Result  {
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

		go func ()  {
			defer wg.Done()

			s.fastWorker(
				ctx,
				jobs,
				results,
			)
		}()
	}

	go func ()  {
		defer close(jobs)

		for i := 0; i < s.Wordlist.Len(); i++ {
			select{
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