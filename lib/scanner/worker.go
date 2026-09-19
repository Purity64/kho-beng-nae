package scanner

import (
	"context"
	"io"
	"net/http"
	"time"
)

func (s *Scanner) worker(
	ctx context.Context,
	jobs <-chan uint32,
	results chan<- Result,
) {

	for pathIndex := range jobs {

		start := time.Now()

		path := s.Wordlist.Get(int(pathIndex))

		cleanPath := path

		for len(cleanPath) > 0 &&
			cleanPath[0] == '/' {

			cleanPath = cleanPath[1:]
		}

		target := s.BaseURL + "/" + string(cleanPath)

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			target,
			nil,
		)

		if err != nil {

			results <- Result{
				PathIndex: pathIndex,
				Duration:  time.Since(start),
				Err:       err,
			}

			continue
		}

		resp, err := s.Client.Do(req)

		if err != nil {

			results <- Result{
				PathIndex: pathIndex,
				Duration:  time.Since(start),
				Err:       err,
			}

			continue
		}

		_, _ = io.Copy(
			io.Discard,
			resp.Body,
		)

		resp.Body.Close()

		results <- Result{
			PathIndex:  pathIndex,
			StatusCode: uint16(resp.StatusCode),
			Size:       resp.ContentLength,
			Duration:   time.Since(start),
		}
	}
}