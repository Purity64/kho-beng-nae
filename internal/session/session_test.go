package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"Kho-beng-nae/internal/discover"
	"Kho-beng-nae/internal/engine"
)

type fakeDoer struct {
	mu     sync.Mutex
	routes map[string]*engine.Response
}

func (f *fakeDoer) Do(_ context.Context, url string) (*engine.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.routes[url]; ok {
		cp := *r
		return &cp, nil
	}
	return &engine.Response{StatusCode: 404, Size: 10}, nil
}

type recorder struct {
	mu     sync.Mutex
	states []string
	hits   int
}

func (r *recorder) Emit(event string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch event {
	case EvState:
		if m, ok := data.(map[string]any); ok {
			r.states = append(r.states, string(m["state"].(State)))
		}
	case EvHit:
		r.hits++
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout waiting for condition")
}

func TestManagerLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	words := []string{"admin/", "login", "ghost"}
	m := NewManager(words, rec, store)

	f := &fakeDoer{routes: map[string]*engine.Response{
		"http://x/admin/":      {StatusCode: 200, Size: 500},
		"http://x/login":       {StatusCode: 200, Size: 100},
		"http://x/admin/login": {StatusCode: 200, Size: 100},
	}}
	m.SetTestDoer(f)

	sess, err := m.Create("target", "http://x", ScanParams{Recurse: true, MaxDepth: 1, Retries: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(sess.ID, ScanParams{Recurse: true, MaxDepth: 1, Retries: 0}); err != nil {
		t.Fatal(err)
	}

	// Wait for completion.
	waitFor(t, func() bool {
		s, _ := m.Get(sess.ID)
		return s.State == StateDone
	})

	got, _ := m.Get(sess.ID)
	if len(got.Hits) < 2 {
		t.Errorf("expected >=2 hits, got %d", len(got.Hits))
	}
	// Recursion: admin/login should be present.
	found := false
	for _, h := range got.Hits {
		if h.Path == "admin/login" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected recursive hit admin/login; hits=%v", got.Hits)
	}

	// Persistence: a fresh manager over the same dir sees the session.
	m2 := NewManager(words, NopEmitter{}, store)
	if _, err := m2.Get(sess.ID); err != nil {
		t.Errorf("session not persisted: %v", err)
	}

	// Delete removes it.
	if err := m.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(sess.ID); err == nil {
		t.Errorf("expected session gone after delete")
	}
}

func TestManagerStop(t *testing.T) {
	m := NewManager([]string{"a", "b", "c"}, NopEmitter{}, nil)
	// Slow doer so the scan is still running when we stop it.
	f := &slowFake{delay: 30 * time.Millisecond}
	m.SetTestDoer(f)

	sess, _ := m.Create("t", "http://y", ScanParams{})
	if err := m.Start(sess.ID, ScanParams{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := m.Stop(sess.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s, _ := m.Get(sess.ID)
		return s.State == StateStopped
	})
}

type fakeBody struct{ m map[string]*discover.Body }

func (f *fakeBody) Get(_ context.Context, url string) (*discover.Body, error) {
	if b, ok := f.m[url]; ok {
		return b, nil
	}
	return &discover.Body{Status: 404, ContentType: "text/plain", Data: []byte("no")}, nil
}

func TestDiscoverThenSeedScan(t *testing.T) {
	// Discovery finds /w/clock; the brute-force is then seeded from it and
	// scans /w and /w/clock, so it should surface w/clock and w/clock/secret.
	body := &fakeBody{m: map[string]*discover.Body{
		"http://x/": {Status: 200, ContentType: "text/html",
			Data: []byte(`<a href="/w/clock">clock</a>`)},
	}}
	doer := &fakeDoer{routes: map[string]*engine.Response{
		"http://x/w/clock":        {StatusCode: 200, Size: 100},
		"http://x/w/clock/secret": {StatusCode: 200, Size: 90},
	}}

	m := NewManager([]string{"clock", "secret"}, NopEmitter{}, nil)
	m.SetTestDoer(doer)
	m.SetTestBodyDoer(body)

	sess, _ := m.Create("t", "http://x", ScanParams{})
	err := m.Start(sess.ID, ScanParams{
		Mode: ModeBoth, Crawl: true, MineJS: false, UseRobots: false,
		Recurse: false, MaxDepth: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s, _ := m.Get(sess.ID)
		return s.State == StateDone
	})

	s, _ := m.Get(sess.ID)
	// endpoint discovered
	foundEp := false
	for _, e := range s.Endpoints {
		if e.Path == "w/clock" {
			foundEp = true
		}
	}
	if !foundEp {
		t.Errorf("expected discovered endpoint w/clock, got %v", s.Endpoints)
	}
	// seeded brute-force hits
	hits := map[string]bool{}
	for _, h := range s.Hits {
		hits[h.Path] = true
	}
	if !hits["w/clock"] || !hits["w/clock/secret"] {
		t.Errorf("expected seeded hits w/clock and w/clock/secret, got %v", s.Hits)
	}
}

func TestDoerFactoryUsed(t *testing.T) {
	// With no test doer, the factory must be consulted and its transport used.
	f := &fakeDoer{routes: map[string]*engine.Response{
		"http://127.0.0.1:1/hit": {StatusCode: 200, Size: 10},
	}}
	var called bool
	m := NewManager([]string{"hit"}, NopEmitter{}, nil)
	m.SetDoerFactory(func(_ engine.Protocol, _ engine.TransportConfig) engine.Doer {
		called = true
		return f
	})
	m.SetTestBodyDoer(&fakeBody{m: map[string]*discover.Body{}})

	sess, _ := m.Create("t", "http://127.0.0.1:1", ScanParams{Mode: ModeBruteforce, Timeout: 1})
	if err := m.Start(sess.ID, ScanParams{Mode: ModeBruteforce, Timeout: 1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		s, _ := m.Get(sess.ID)
		return s.State == StateDone
	})
	if !called {
		t.Fatal("doer factory was not called")
	}
	s, _ := m.Get(sess.ID)
	found := false
	for _, h := range s.Hits {
		if h.Path == "hit" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected factory doer to produce hit, got %v", s.Hits)
	}
}

type slowFake struct{ delay time.Duration }

func (s *slowFake) Do(ctx context.Context, _ string) (*engine.Response, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(s.delay):
	}
	return &engine.Response{StatusCode: 404, Size: 10}, nil
}
