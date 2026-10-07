package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---- fake transport -------------------------------------------------------

type stub struct {
	resp      *Response
	err       error
	failFirst int // fail this many times (transport error) before succeeding
}

type fakeDoer struct {
	mu       sync.Mutex
	routes   map[string]*stub
	fallback *Response // returned for unknown URLs (e.g. a 404)
	calls    map[string]int
}

func newFakeDoer() *fakeDoer {
	return &fakeDoer{
		routes:   map[string]*stub{},
		fallback: &Response{StatusCode: 404, Size: 20},
		calls:    map[string]int{},
	}
}

func (f *fakeDoer) route(url string, s *stub) { f.routes[url] = s }

func (f *fakeDoer) Do(_ context.Context, url string) (*Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[url]++
	if s, ok := f.routes[url]; ok {
		if s.failFirst > 0 {
			s.failFirst--
			return nil, errors.New("transport boom")
		}
		if s.err != nil {
			return nil, s.err
		}
		return s.resp, nil
	}
	r := *f.fallback
	return &r, nil
}

// ---- collecting sink ------------------------------------------------------

type collectSink struct {
	mu       sync.Mutex
	hits     []Hit
	dirs     []string
	errFinal []string
	errRetry int
}

func (c *collectSink) OnHit(h Hit)        { c.mu.Lock(); c.hits = append(c.hits, h); c.mu.Unlock() }
func (c *collectSink) OnProgress(Metrics) {}
func (c *collectSink) OnDirEnter(p string, _ int) {
	c.mu.Lock()
	c.dirs = append(c.dirs, p)
	c.mu.Unlock()
}
func (c *collectSink) OnProbeError(p string, _ error, willRetry bool) {
	c.mu.Lock()
	if willRetry {
		c.errRetry++
	} else {
		c.errFinal = append(c.errFinal, p)
	}
	c.mu.Unlock()
}

func (c *collectSink) hitPaths() map[string]Hit {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]Hit{}
	for _, h := range c.hits {
		m[h.Path] = h
	}
	return m
}

// ---- tests ----------------------------------------------------------------

func TestCategorize(t *testing.T) {
	cases := map[int]Category{
		200: CatSuccess, 204: CatSuccess,
		301: CatRedirect, 302: CatRedirect,
		401: CatAuth, 403: CatAuth,
		404: CatClientErr, 418: CatClientErr,
		500: CatServerErr, 503: CatServerErr,
	}
	for status, want := range cases {
		if got := Categorize(status); got != want {
			t.Errorf("Categorize(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestRecursiveDiscovery(t *testing.T) {
	f := newFakeDoer()
	base := "http://x"
	// /admin is a directory (200, trailing-slash word).
	f.route(base+"/admin/", &stub{resp: &Response{StatusCode: 200, Size: 500}})
	// /login is a plain file hit.
	f.route(base+"/login", &stub{resp: &Response{StatusCode: 200, Size: 120}})
	// under /admin: /admin/secret is protected (403).
	f.route(base+"/admin/secret", &stub{resp: &Response{StatusCode: 403, Size: 90}})

	sink := &collectSink{}
	cfg := Config{
		BaseURL:        base,
		Words:          []string{"admin/", "login", "secret", "nope"},
		Found:          FoundPolicy{},
		Adaptive:       AdaptiveConfig{Min: 1, Max: 4, Start: 4, Batch: 5},
		Recurse:        true,
		MaxDepth:       2,
		Retries:        0,
		BaselineProbes: 3,
	}
	r := NewRunner(cfg, sink, f)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	hits := sink.hitPaths()
	if _, ok := hits["admin/"]; !ok {
		t.Errorf("expected hit on admin/, got %v", keys(hits))
	}
	if _, ok := hits["login"]; !ok {
		t.Errorf("expected hit on login")
	}
	sec, ok := hits["admin/secret"]
	if !ok {
		t.Fatalf("expected recursion to find admin/secret, hits=%v", keys(hits))
	}
	if sec.Category != CatAuth || sec.Depth != 1 {
		t.Errorf("admin/secret: cat=%v depth=%d, want auth/1", sec.Category, sec.Depth)
	}
	if got := hits["admin/"]; !got.IsDir {
		t.Errorf("admin/ should be flagged IsDir")
	}
}

func TestExpandSeeds(t *testing.T) {
	got := expandSeeds([]string{"w/clock", "/api/v1/users/", "w"})
	want := []string{"api", "w", "api/v1", "w/clock", "api/v1/users"}
	// order: depth 0 first (api, w), then depth 1 (api/v1, w/clock), then depth 2
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expandSeeds order = %v, want %v", got, want)
		}
	}
}

func TestSeedRoots(t *testing.T) {
	f := newFakeDoer()
	base := "http://x"
	f.route(base+"/w/clock", &stub{resp: &Response{StatusCode: 200, Size: 100}})
	f.route(base+"/w/clock/secret", &stub{resp: &Response{StatusCode: 200, Size: 80}})

	sink := &collectSink{}
	cfg := Config{
		BaseURL:        base,
		Words:          []string{"clock", "secret"},
		Adaptive:       AdaptiveConfig{Min: 1, Max: 4, Start: 4, Batch: 5},
		Recurse:        false,
		MaxDepth:       0,
		BaselineProbes: 0,
		SeedDirs:       []string{"w/clock"},
	}
	r := NewRunner(cfg, sink, f)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Both ancestor levels must have been scanned.
	dirs := map[string]bool{}
	sink.mu.Lock()
	for _, d := range sink.dirs {
		dirs[d] = true
	}
	sink.mu.Unlock()
	if !dirs["w"] || !dirs["w/clock"] {
		t.Errorf("expected scans of w and w/clock, got dirs %v", sink.dirs)
	}

	hits := sink.hitPaths()
	if _, ok := hits["w/clock"]; !ok {
		t.Errorf("expected hit w/clock (from scanning w), got %v", keys(hits))
	}
	if _, ok := hits["w/clock/secret"]; !ok {
		t.Errorf("expected hit w/clock/secret (from scanning w/clock), got %v", keys(hits))
	}
}

func TestRetryThenSucceed(t *testing.T) {
	f := newFakeDoer()
	base := "http://x"
	// /flaky fails twice, then returns 200.
	f.route(base+"/flaky", &stub{failFirst: 2, resp: &Response{StatusCode: 200, Size: 77}})

	sink := &collectSink{}
	cfg := Config{
		BaseURL:        base,
		Words:          []string{"flaky"},
		Adaptive:       AdaptiveConfig{Min: 1, Max: 2, Start: 2, Batch: 10},
		Retries:        3,
		BaselineProbes: 0,
	}
	r := NewRunner(cfg, sink, f)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := sink.hitPaths()["flaky"]; !ok {
		t.Errorf("flaky should be a hit after retries; final errors=%v", sink.errFinal)
	}
	if sink.errRetry < 2 {
		t.Errorf("expected >=2 retry notices, got %d", sink.errRetry)
	}
}

func TestRetryExhausted(t *testing.T) {
	f := newFakeDoer()
	base := "http://x"
	f.route(base+"/dead", &stub{err: errors.New("always down")})

	sink := &collectSink{}
	cfg := Config{
		BaseURL:        base,
		Words:          []string{"dead"},
		Adaptive:       AdaptiveConfig{Min: 1, Max: 1, Start: 1, Batch: 10},
		Retries:        2,
		BaselineProbes: 0,
	}
	r := NewRunner(cfg, sink, f)
	_ = r.Run(context.Background())

	if len(sink.errFinal) != 1 || sink.errFinal[0] != "dead" {
		t.Errorf("expected one final error for dead, got %v", sink.errFinal)
	}
	// 1 initial + 2 retries = 3 attempts.
	if got := f.calls[base+"/dead"]; got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestSoftFourOhFour(t *testing.T) {
	f := newFakeDoer()
	base := "http://x"
	// Wildcard target: every unknown path returns 200 with ~1000 bytes.
	f.fallback = &Response{StatusCode: 200, Size: 1000}
	// A genuine page is 200 but a distinctly different size.
	f.route(base+"/real", &stub{resp: &Response{StatusCode: 200, Size: 4000}})

	bl, err := Learn(context.Background(), f, base, FoundPolicy{}, 5)
	if err != nil {
		t.Fatalf("Learn: %v", err)
	}
	if !bl.Wildcard {
		t.Fatalf("expected wildcard baseline")
	}
	if !bl.IsSoft404(&Response{StatusCode: 200, Size: 1000}) {
		t.Errorf("catch-all sized response should be soft404")
	}
	if bl.IsSoft404(&Response{StatusCode: 200, Size: 4000}) {
		t.Errorf("real page should NOT be soft404")
	}

	// End to end: only /real should surface as a hit.
	sink := &collectSink{}
	cfg := Config{
		BaseURL:        base,
		Words:          []string{"real", "ghost1", "ghost2"},
		Adaptive:       AdaptiveConfig{Min: 1, Max: 3, Start: 3, Batch: 10},
		BaselineProbes: 5,
	}
	r := NewRunner(cfg, sink, f)
	_ = r.Run(context.Background())
	hits := sink.hitPaths()
	if _, ok := hits["real"]; !ok {
		t.Errorf("real should be a hit")
	}
	if _, ok := hits["ghost1"]; ok {
		t.Errorf("ghost1 should be filtered as soft404")
	}
}

func TestAdaptiveRampAndBackoff(t *testing.T) {
	ctrl := NewAdaptiveController(AdaptiveConfig{Min: 2, Max: 50, Start: 10, Batch: 10, ErrHigh: 0.10, BackoffPc: 50})

	// Two clean batches -> +1 each.
	for i := 0; i < 20; i++ {
		ctrl.Record(false)
	}
	if got := ctrl.Metrics().Concurrency; got != 12 {
		t.Errorf("after 2 clean batches want 12, got %d", got)
	}

	// A batch with 30% errors -> multiplicative decrease (12 * 50% = 6).
	for i := 0; i < 7; i++ {
		ctrl.Record(false)
	}
	for i := 0; i < 3; i++ {
		ctrl.Record(true)
	}
	if got := ctrl.Metrics().Concurrency; got != 6 {
		t.Errorf("after error batch want 6, got %d", got)
	}
}

func TestStopViaContext(t *testing.T) {
	f := newFakeDoer()
	f.fallback = &Response{StatusCode: 404, Size: 10}
	base := "http://x"
	// Slow responses so cancellation lands mid-scan.
	slow := &slowDoer{inner: f, delay: 20 * time.Millisecond}

	words := make([]string, 500)
	for i := range words {
		words[i] = "w" + itoa(i)
	}
	sink := &collectSink{}
	cfg := Config{
		BaseURL:        base,
		Words:          words,
		Adaptive:       AdaptiveConfig{Min: 1, Max: 4, Start: 4, Batch: 50},
		BaselineProbes: 0,
	}
	r := NewRunner(cfg, sink, slow)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(40 * time.Millisecond); cancel() }()
	err := r.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

type slowDoer struct {
	inner Doer
	delay time.Duration
}

func (s *slowDoer) Do(ctx context.Context, url string) (*Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.delay):
	}
	return s.inner.Do(ctx, url)
}

// ---- helpers --------------------------------------------------------------

func keys(m map[string]Hit) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
