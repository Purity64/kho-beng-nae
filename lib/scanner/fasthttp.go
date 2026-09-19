package scanner

import (
	"context"
	"time"

	"github.com/valyala/fasthttp"
)

func (s *ScannerFast) fastWorker(
	ctx context.Context,
	jobs <-chan uint32,
	results chan<- Result,
) {

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()

	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodGet)

	for pathIndex := range jobs {

		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()

		path := s.Wordlist.Get(int(pathIndex))

		for len(path) > 0 && path[0] == '/' {
			path = path[1:]
		}


		resp.Reset()

		target := s.BaseURL + "/" + string(path)

		req.SetRequestURI(target)

		err := s.FastClient.DoTimeout(
			req,
			resp,
			5*time.Second,
		)

		if err != nil {
			results <- Result{
				PathIndex: pathIndex,
				Duration:  time.Since(start),
				Err:       err,
			}

			continue
		}

		results <- Result{
			PathIndex:  pathIndex,
			StatusCode: uint16(resp.StatusCode()),
			Size:       int64(len(resp.Body())),
			Duration:   time.Since(start),
		}
	}
}