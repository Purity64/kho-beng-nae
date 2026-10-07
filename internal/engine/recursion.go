package engine

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config fully describes a scan.
type Config struct {
	BaseURL        string
	Words          []string
	Found          FoundPolicy
	Adaptive       AdaptiveConfig
	Transport      TransportConfig
	Recurse        bool
	MaxDepth       int // maximum recursion depth (0 = root level only)
	Retries        int // transport-error retries per path
	BaselineProbes int // random probes per directory (0 disables soft-404)

	// SeedDirs are directories to brute-force in addition to the root. Each
	// is a path discovered elsewhere (e.g. "w/clock" found by the crawler);
	// the runner expands it into its ancestor chain and scans every level, so
	// "w/clock" seeds a scan of "w" (siblings of clock) and then "w/clock"
	// (its children). Seeds are scanned even when past MaxDepth; recursion
	// below a seed still respects MaxDepth.
	SeedDirs []string
}

func (c Config) withDefaults() Config {
	if c.BaselineProbes == 0 {
		c.BaselineProbes = 3
	}
	if c.Retries == 0 {
		c.Retries = 2
	}
	return c
}

type dirItem struct {
	path  string
	depth int
}

type job struct {
	base     string
	dirPath  string
	word     string
	depth    int
	baseline Baseline
}

// Runner executes one recursive scan with a single persistent worker pool that
// spans every directory: as soon as a directory's jobs are queued the workers
// pull from them, and newly discovered subdirectories are fed into the same
// queue, so workers never idle waiting for the next directory. Create it with
// NewRunner and drive it with Run. It is single-use.
type Runner struct {
	cfg  Config
	doer Doer
	ctrl *AdaptiveController
	sink Sink

	// mu guards frontier, visited and outstanding; cond wakes the producer.
	mu          sync.Mutex
	cond        *sync.Cond
	frontier    []dirItem
	visited     map[string]bool
	outstanding int // jobs queued or in flight but not yet completed

	jobs chan job

	lastEmit   time.Time
	lastEmitMu sync.Mutex
}

// NewRunner builds a Runner. If doer is nil a net/http Doer is created from
// cfg.Transport.
func NewRunner(cfg Config, sink Sink, doer Doer) *Runner {
	cfg = cfg.withDefaults()
	if doer == nil {
		doer = NewHTTPDoer(cfg.Transport)
	}
	r := &Runner{
		cfg:     cfg,
		doer:    doer,
		ctrl:    NewAdaptiveController(cfg.Adaptive),
		sink:    sink,
		visited: make(map[string]bool),
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Controller exposes the adaptive controller (for live metrics).
func (r *Runner) Controller() *AdaptiveController { return r.ctrl }

// markVisitedLocked records a directory as scanned; returns false if already
// done. Caller must hold r.mu.
func (r *Runner) markVisitedLocked(path string) bool {
	key := strings.Trim(path, "/")
	if r.visited[key] {
		return false
	}
	r.visited[key] = true
	return true
}

// enqueueSubdir adds a newly discovered directory to the frontier and wakes
// the producer. Called from workers.
func (r *Runner) enqueueSubdir(path string, depth int) {
	r.mu.Lock()
	if r.markVisitedLocked(path) {
		r.frontier = append(r.frontier, dirItem{path: path, depth: depth})
		r.cond.Signal()
	}
	r.mu.Unlock()
}

// Run performs the scan. Workers run continuously; a producer walks the
// frontier, opens each directory (learns its soft-404 baseline) and queues its
// word jobs, appending discovered subdirectories to the same frontier until it
// drains. Returns ctx.Err() when cancelled, otherwise nil.
func (r *Runner) Run(ctx context.Context) error {
	// Seed the frontier: root first, then the ancestor chain of every
	// discovered endpoint, shallowest first.
	r.frontier = append(r.frontier, dirItem{path: "", depth: 0})
	r.markVisitedLocked("")
	for _, dir := range expandSeeds(r.cfg.SeedDirs) {
		if r.markVisitedLocked(dir) {
			r.frontier = append(r.frontier, dirItem{path: dir, depth: strings.Count(dir, "/") + 1})
		}
	}

	workers := r.cfg.Adaptive.Max
	if workers < 1 {
		workers = 1
	}
	r.jobs = make(chan job, workers*4)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := range r.jobs {
				r.probe(ctx, j)
				r.mu.Lock()
				r.outstanding--
				if r.outstanding == 0 {
					r.cond.Broadcast()
				}
				r.mu.Unlock()
			}
		}()
	}

	r.produce(ctx)
	close(r.jobs)
	wg.Wait()

	r.sink.OnProgress(r.ctrl.Metrics())
	return ctx.Err()
}

// produce walks the frontier and queues jobs. It runs in the calling
// goroutine until the frontier is empty and no jobs remain outstanding.
func (r *Runner) produce(ctx context.Context) {
	for {
		r.mu.Lock()
		for len(r.frontier) == 0 && r.outstanding > 0 && ctx.Err() == nil {
			r.cond.Wait()
		}
		if ctx.Err() != nil {
			r.mu.Unlock()
			return
		}
		if len(r.frontier) == 0 && r.outstanding == 0 {
			r.mu.Unlock()
			return
		}
		item := r.frontier[0]
		r.frontier = r.frontier[1:]
		r.mu.Unlock()

		r.openDir(ctx, item)
	}
}

// openDir emits the dir-enter event, learns its baseline, and queues one job
// per word.
func (r *Runner) openDir(ctx context.Context, item dirItem) {
	r.sink.OnDirEnter(item.path, item.depth)

	base := strings.TrimRight(r.cfg.BaseURL, "/")
	if item.path != "" {
		base += "/" + strings.Trim(item.path, "/")
	}

	var baseline Baseline
	if r.cfg.BaselineProbes != 0 {
		baseline, _ = Learn(ctx, r.doer, base, r.cfg.Found, r.cfg.BaselineProbes)
	}

	// Reserve outstanding for all words up front so termination cannot trigger
	// while we are still queueing this directory.
	r.mu.Lock()
	r.outstanding += len(r.cfg.Words)
	r.mu.Unlock()

	queued := 0
	for _, w := range r.cfg.Words {
		select {
		case <-ctx.Done():
			// Give back the words we never queued.
			r.mu.Lock()
			r.outstanding -= len(r.cfg.Words) - queued
			if r.outstanding == 0 {
				r.cond.Broadcast()
			}
			r.mu.Unlock()
			return
		case r.jobs <- job{base: base, dirPath: item.path, word: w, depth: item.depth, baseline: baseline}:
			queued++
		}
	}
}

// expandSeeds turns a list of discovered paths into the deduplicated set of
// their ancestor prefixes, ordered shallowest first. "w/clock" ->
// ["w", "w/clock"]; combined across all seeds and sorted by depth so parents
// are always scanned before children.
func expandSeeds(seeds []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range seeds {
		s = strings.Trim(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		segs := strings.Split(s, "/")
		for i := 1; i <= len(segs); i++ {
			prefix := strings.Join(segs[:i], "/")
			if !seen[prefix] {
				seen[prefix] = true
				out = append(out, prefix)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		di := strings.Count(out[i], "/")
		dj := strings.Count(out[j], "/")
		if di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

// emitProgress pushes a metrics snapshot to the sink, throttled to at most
// once per 200ms so the UI is not flooded.
func (r *Runner) emitProgress() {
	r.lastEmitMu.Lock()
	now := time.Now()
	if now.Sub(r.lastEmit) < 200*time.Millisecond {
		r.lastEmitMu.Unlock()
		return
	}
	r.lastEmit = now
	r.lastEmitMu.Unlock()
	r.sink.OnProgress(r.ctrl.Metrics())
}
