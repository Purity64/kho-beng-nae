package engine

import (
	"context"
	"strings"
	"time"
)

// Hit is a discovered path.
type Hit struct {
	Path     string        `json:"path"`     // path relative to root, e.g. "admin/config"
	URL      string        `json:"url"`      // absolute URL
	Status   int           `json:"status"`   // HTTP status code
	Category Category      `json:"category"` // grouped category for the UI
	Size     int64         `json:"size"`     // response size (bytes)
	Duration time.Duration `json:"duration"` // round-trip time (ns)
	IsDir    bool          `json:"isDir"`    // plausible directory (recursion candidate)
	Depth    int           `json:"depth"`    // recursion depth (0 = root)
}

// Sink receives live events from a running scan. All methods may be called
// concurrently from worker goroutines; implementations must be safe for that.
type Sink interface {
	OnHit(Hit)
	OnProgress(Metrics)
	OnDirEnter(path string, depth int)
	// OnProbeError reports a transport failure for one path. willRetry is true
	// when the path will be probed again; the final call for a path that keeps
	// failing has willRetry=false.
	OnProbeError(path string, err error, willRetry bool)
}

// cleanWord strips leading slashes so join produces exactly one separator.
func cleanWord(w string) string {
	for len(w) > 0 && w[0] == '/' {
		w = w[1:]
	}
	return strings.TrimSpace(w)
}

// joinPath joins a dir path and a word with a single slash.
func joinPath(dir, word string) string {
	dir = strings.Trim(dir, "/")
	word = cleanWord(word)
	if dir == "" {
		return word
	}
	if word == "" {
		return dir
	}
	return dir + "/" + word
}

// probe performs one path probe with adaptive gating and transport-error
// retries, emitting hits through the sink and feeding discovered directories
// back into the frontier.
func (r *Runner) probe(ctx context.Context, j job) {
	word := cleanWord(j.word)
	if word == "" {
		return
	}
	fullPath := joinPath(j.dirPath, word)
	target := j.base + "/" + word

	attempts := r.cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		if err := r.ctrl.Acquire(ctx); err != nil {
			return
		}
		resp, err := r.doer.Do(ctx, target)
		r.ctrl.Release()

		if err != nil {
			r.ctrl.Record(true)
			willRetry := attempt < attempts-1 && ctx.Err() == nil
			r.sink.OnProbeError(fullPath, err, willRetry)
			if willRetry {
				r.ctrl.RecordRetry()
				// Small backoff before re-checking, as requested: an errored
				// path is verified again rather than dropped.
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff(attempt)):
				}
				continue
			}
			r.emitProgress()
			return
		}

		r.ctrl.Record(false)

		if r.cfg.Found.IsFound(resp.StatusCode) && !j.baseline.IsSoft404(resp) {
			isDir := LooksLikeDirectory(fullPath, resp)
			r.sink.OnHit(Hit{
				Path:     fullPath,
				URL:      target,
				Status:   resp.StatusCode,
				Category: Categorize(resp.StatusCode),
				Size:     resp.Size,
				Duration: resp.Duration,
				IsDir:    isDir,
				Depth:    j.depth,
			})
			if isDir && r.cfg.Recurse && j.depth < r.cfg.MaxDepth {
				r.enqueueSubdir(fullPath, j.depth+1)
			}
		}
		r.emitProgress()
		return
	}
}

func backoff(attempt int) time.Duration {
	d := time.Duration(100<<attempt) * time.Millisecond
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}
