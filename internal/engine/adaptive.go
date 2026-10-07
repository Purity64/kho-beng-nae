package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// dynsem is a semaphore whose limit can change at runtime. Acquire blocks
// until the number of in-flight holders is below the current limit.
type dynsem struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int
	inUse int
}

func newDynSem(limit int) *dynsem {
	s := &dynsem{limit: limit}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *dynsem) acquire(ctx context.Context) error {
	// Wake the waiter if the context is cancelled while it is parked.
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	for s.inUse >= s.limit {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.cond.Wait()
	}
	s.inUse++
	return nil
}

func (s *dynsem) release() {
	s.mu.Lock()
	s.inUse--
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *dynsem) setLimit(n int) {
	s.mu.Lock()
	s.limit = n
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *dynsem) currentLimit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limit
}

// AdaptiveConfig controls the staircase concurrency search.
type AdaptiveConfig struct {
	Min       int     // floor for concurrency (>=1)
	Max       int     // ceiling for concurrency
	Start     int     // starting concurrency
	Batch     int     // completions between adjustments (e.g. 30)
	ErrHigh   float64 // batch error rate that triggers a back-off (e.g. 0.10)
	BackoffPc int     // percent of current kept on back-off (e.g. 75 => *0.75)
}

func (c AdaptiveConfig) withDefaults() AdaptiveConfig {
	if c.Min < 1 {
		c.Min = 1
	}
	if c.Max < c.Min {
		c.Max = 100
	}
	if c.Start < c.Min {
		c.Start = c.Min
	}
	if c.Start > c.Max {
		c.Start = c.Max
	}
	if c.Batch < 1 {
		c.Batch = 30
	}
	if c.ErrHigh <= 0 {
		c.ErrHigh = 0.10
	}
	if c.BackoffPc <= 0 || c.BackoffPc >= 100 {
		c.BackoffPc = 75
	}
	return c
}

// Metrics is a snapshot of the controller state for the UI.
type Metrics struct {
	Concurrency int     `json:"concurrency"`
	InFlight    int     `json:"inFlight"`
	Total       int64   `json:"total"`
	Errors      int64   `json:"errors"`
	Retries     int64   `json:"retries"`
	LastErrRate float64 `json:"lastErrRate"`
	RatePerSec  float64 `json:"ratePerSec"` // instantaneous throughput
	AvgPerSec   float64 `json:"avgPerSec"`  // average since start
}

// AdaptiveController implements additive-increase / multiplicative-decrease
// concurrency control (the same idea as TCP congestion control):
//
//   - every Batch completions with a clean error rate -> concurrency += 1
//   - a batch whose error rate exceeds ErrHigh          -> concurrency *= 0.75
//
// so it ramps up while the target keeps up and steps back the moment errors
// appear, settling near the fastest stable rate.
type AdaptiveController struct {
	cfg AdaptiveConfig
	sem *dynsem

	mu         sync.Mutex
	batchTotal int
	batchErr   int
	lastRate   float64

	// throughput sampling (guarded by mu)
	startTime      time.Time
	lastSampleTime time.Time
	lastSampleTot  int64
	lastLive       float64

	total   atomic.Int64
	errors  atomic.Int64
	retries atomic.Int64
}

func NewAdaptiveController(cfg AdaptiveConfig) *AdaptiveController {
	cfg = cfg.withDefaults()
	now := time.Now()
	return &AdaptiveController{
		cfg:            cfg,
		sem:            newDynSem(cfg.Start),
		startTime:      now,
		lastSampleTime: now,
	}
}

// Acquire blocks until a concurrency slot is free.
func (a *AdaptiveController) Acquire(ctx context.Context) error {
	return a.sem.acquire(ctx)
}

// Release returns a slot.
func (a *AdaptiveController) Release() { a.sem.release() }

// Record reports the outcome of one completed probe (a transport error, not
// an HTTP status) and adjusts concurrency when a batch closes.
func (a *AdaptiveController) Record(transportErr bool) {
	a.total.Add(1)
	if transportErr {
		a.errors.Add(1)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.batchTotal++
	if transportErr {
		a.batchErr++
	}
	if a.batchTotal < a.cfg.Batch {
		return
	}

	rate := float64(a.batchErr) / float64(a.batchTotal)
	a.lastRate = rate
	cur := a.sem.currentLimit()

	switch {
	case rate > a.cfg.ErrHigh:
		// Multiplicative decrease.
		next := cur * a.cfg.BackoffPc / 100
		if next < a.cfg.Min {
			next = a.cfg.Min
		}
		if next != cur {
			a.sem.setLimit(next)
		}
	case rate == 0 && cur < a.cfg.Max:
		// Additive increase, only on a perfectly clean batch.
		a.sem.setLimit(cur + 1)
	}

	a.batchTotal = 0
	a.batchErr = 0
}

// RecordRetry notes that a path was re-queued after a transport error.
func (a *AdaptiveController) RecordRetry() { a.retries.Add(1) }

// Metrics returns a snapshot for the UI, including throughput. The
// instantaneous rate is sampled between successive Metrics calls, so it
// reflects the requests completed since the previous snapshot.
func (a *AdaptiveController) Metrics() Metrics {
	total := a.total.Load()
	now := time.Now()

	a.mu.Lock()
	rate := a.lastRate

	live := a.lastLive
	if dt := now.Sub(a.lastSampleTime).Seconds(); dt >= 0.05 {
		live = float64(total-a.lastSampleTot) / dt
		a.lastSampleTime = now
		a.lastSampleTot = total
		a.lastLive = live
	}
	avg := 0.0
	if el := now.Sub(a.startTime).Seconds(); el > 0 {
		avg = float64(total) / el
	}
	a.mu.Unlock()

	a.sem.mu.Lock()
	inflight := a.sem.inUse
	limit := a.sem.limit
	a.sem.mu.Unlock()
	return Metrics{
		Concurrency: limit,
		InFlight:    inflight,
		Total:       total,
		Errors:      a.errors.Load(),
		Retries:     a.retries.Load(),
		LastErrRate: rate,
		RatePerSec:  live,
		AvgPerSec:   avg,
	}
}
