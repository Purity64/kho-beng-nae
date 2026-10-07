package session

import (
	"context"
	"sync"
	"time"

	"Kho-beng-nae/internal/discover"
	"Kho-beng-nae/internal/engine"
)

// State is the lifecycle state of a session's scan.
type State string

const (
	StateIdle    State = "idle"
	StateRunning State = "running"
	StateStopped State = "stopped"
	StateDone    State = "done"
	StateError   State = "error"
)

// Event names emitted to the UI.
const (
	EvHit      = "session:hit"
	EvMetrics  = "session:metrics"
	EvState    = "session:state"
	EvDir      = "session:dir"
	EvError    = "session:error"
	EvEndpoint = "session:endpoint"
	EvPhase    = "session:phase"
	EvTimer    = "session:timer"

	EvWLLoading = "wordlist:loading"
	EvWLReady   = "wordlist:ready"
)

// BuiltinWL is the reserved name of the embedded default wordlist.
const BuiltinWL = "built-in"

// Emitter delivers live events to the UI. app.go wires this to Wails runtime
// events; tests pass a no-op or a recorder.
type Emitter interface {
	Emit(event string, data any)
}

// NopEmitter discards events.
type NopEmitter struct{}

func (NopEmitter) Emit(string, any) {}

// Session holds one target and the results discovered for it.
type Session struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	BaseURL   string    `json:"baseUrl"`
	Protocol  string    `json:"protocol"`
	State     State     `json:"state"`
	Err       string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"createdAt"`

	// Scan parameters echoed for the UI (the wordlist itself is not stored).
	MaxDepth int    `json:"maxDepth"`
	Recurse  bool   `json:"recurse"`
	Mode     string `json:"mode"` // bruteforce | discover | both

	Hits      []engine.Hit        `json:"hits"`
	Endpoints []discover.Endpoint `json:"endpoints"`
	Metrics   engine.Metrics      `json:"metrics"`

	StartedAt time.Time `json:"startedAt"`
	ElapsedMs int64     `json:"elapsedMs"` // final duration once finished

	// runtime-only (not persisted)
	cancel  context.CancelFunc
	running bool
	mu      *sync.Mutex
	emit    Emitter
}

// lock returns the session's mutex, lazily creating it for sessions rebuilt
// from the store (whose mutex is not serialized).
func (s *Session) lock() *sync.Mutex {
	if s.mu == nil {
		s.mu = &sync.Mutex{}
	}
	return s.mu
}

// snapshot returns a copy safe to hand to the UI or the store. The returned
// value does not carry the live mutex or runtime handles.
func (s *Session) snapshot() Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *s
	cp.mu = nil
	cp.cancel = nil
	cp.emit = nil
	cp.Hits = append([]engine.Hit(nil), s.Hits...)
	cp.Endpoints = append([]discover.Endpoint(nil), s.Endpoints...)
	return cp
}

// addEndpoint records a discovered endpoint and emits it live.
func (s *Session) addEndpoint(e discover.Endpoint) {
	s.mu.Lock()
	s.Endpoints = append(s.Endpoints, e)
	s.mu.Unlock()
	if s.emit != nil {
		s.emit.Emit(EvEndpoint, map[string]any{"id": s.ID, "endpoint": e})
	}
}

// emitPhase tells the UI which stage the session is in ("discovery", "scan").
func (s *Session) emitPhase(phase string) {
	if s.emit != nil {
		s.emit.Emit(EvPhase, map[string]any{"id": s.ID, "phase": phase})
	}
}

// startTimer marks the run start and tells the UI to begin its live clock.
func (s *Session) startTimer() {
	s.mu.Lock()
	s.StartedAt = time.Now()
	s.ElapsedMs = 0
	started := s.StartedAt
	s.mu.Unlock()
	if s.emit != nil {
		s.emit.Emit(EvTimer, map[string]any{"id": s.ID, "startedAt": started.UnixMilli(), "running": true})
	}
}

// stopTimer records the final elapsed time and tells the UI to freeze it.
func (s *Session) stopTimer() {
	s.mu.Lock()
	if !s.StartedAt.IsZero() {
		s.ElapsedMs = time.Since(s.StartedAt).Milliseconds()
	}
	elapsed := s.ElapsedMs
	s.mu.Unlock()
	if s.emit != nil {
		s.emit.Emit(EvTimer, map[string]any{"id": s.ID, "elapsedMs": elapsed, "running": false})
	}
}

func (s *Session) setState(st State, errMsg string) {
	s.mu.Lock()
	s.State = st
	s.Err = errMsg
	s.mu.Unlock()
	if s.emit != nil {
		s.emit.Emit(EvState, map[string]any{"id": s.ID, "state": st, "error": errMsg})
	}
}

// ---- engine.Sink implementation ------------------------------------------

func (s *Session) OnHit(h engine.Hit) {
	s.mu.Lock()
	s.Hits = append(s.Hits, h)
	s.mu.Unlock()
	if s.emit != nil {
		s.emit.Emit(EvHit, map[string]any{"id": s.ID, "hit": h})
	}
}

func (s *Session) OnProgress(m engine.Metrics) {
	s.mu.Lock()
	s.Metrics = m
	s.mu.Unlock()
	if s.emit != nil {
		s.emit.Emit(EvMetrics, map[string]any{"id": s.ID, "metrics": m})
	}
}

func (s *Session) OnDirEnter(path string, depth int) {
	if s.emit != nil {
		s.emit.Emit(EvDir, map[string]any{"id": s.ID, "path": path, "depth": depth})
	}
}

func (s *Session) OnProbeError(path string, err error, willRetry bool) {
	if s.emit != nil && !willRetry {
		s.emit.Emit(EvError, map[string]any{"id": s.ID, "path": path, "error": err.Error()})
	}
}
