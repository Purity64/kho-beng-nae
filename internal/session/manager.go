package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"Kho-beng-nae/internal/discover"
	"Kho-beng-nae/internal/engine"
)

var (
	ErrNotFound = errors.New("session not found")
	ErrRunning  = errors.New("session already running")
)

// Scan modes.
const (
	ModeBruteforce = "bruteforce" // wordlist only
	ModeDiscover   = "discover"   // crawl/JS/robots only
	ModeBoth       = "both"       // discover, then seed brute-force from findings
)

// ScanParams are the per-session tunables the UI can set.
type ScanParams struct {
	Mode      string `json:"mode"`
	Recurse   bool   `json:"recurse"`
	MaxDepth  int    `json:"maxDepth"`
	Retries   int    `json:"retries"`
	StartConc int    `json:"startConcurrency"`
	MaxConc   int    `json:"maxConcurrency"`
	Timeout   int    `json:"timeoutSeconds"`
	SkipTLS   bool   `json:"skipTlsVerify"`

	// discovery options (used for ModeDiscover / ModeBoth)
	Crawl      bool `json:"crawl"`
	MineJS     bool `json:"mineJs"`
	UseRobots  bool `json:"useRobots"`
	CrawlDepth int  `json:"crawlDepth"`
	MaxPages   int  `json:"maxPages"`
}

func (p ScanParams) withDefaults() ScanParams {
	if p.Mode == "" {
		p.Mode = ModeBoth
	}
	if p.MaxDepth <= 0 {
		p.MaxDepth = 2
	}
	if p.Retries < 0 {
		p.Retries = 2
	}
	if p.StartConc <= 0 {
		p.StartConc = 10
	}
	if p.MaxConc <= 0 {
		p.MaxConc = 100
	}
	if p.Timeout <= 0 {
		p.Timeout = 8
	}
	if p.CrawlDepth <= 0 {
		p.CrawlDepth = 3
	}
	if p.MaxPages <= 0 {
		p.MaxPages = 200
	}
	return p
}

// Store persists sessions across restarts.
type Store interface {
	Save(Session) error
	LoadAll() ([]Session, error)
	Delete(id string) error
}

// Manager owns all sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	words    []string
	emit     Emitter
	store    Store

	wlStore  *WordlistStore
	builtin  []string
	activeWL string

	// testDoer, when set, is used instead of building a net/http Doer and
	// protocol detection is skipped. Used by tests to avoid the network.
	testDoer engine.Doer
	// testBody, when set, is used for discovery instead of net/http.
	testBody discover.BodyDoer

	// newDoer, when set, builds the brute-force transport for a detected
	// protocol. app.go wires this to pick fasthttp for HTTP/1 and net/http
	// for HTTP/2. When nil, the runner builds a net/http Doer itself.
	newDoer func(engine.Protocol, engine.TransportConfig) engine.Doer
}

// SetDoerFactory installs a protocol-aware transport factory.
func (m *Manager) SetDoerFactory(f func(engine.Protocol, engine.TransportConfig) engine.Doer) {
	m.newDoer = f
}

// SetTestDoer injects a transport for tests (no network).
func (m *Manager) SetTestDoer(d engine.Doer) { m.testDoer = d }

// SetTestBodyDoer injects a discovery transport for tests (no network).
func (m *Manager) SetTestBodyDoer(d discover.BodyDoer) { m.testBody = d }

// NewManager builds a Manager. words is the shared wordlist; emit and store
// may be nil (NopEmitter / no persistence are substituted).
func NewManager(words []string, emit Emitter, store Store) *Manager {
	if emit == nil {
		emit = NopEmitter{}
	}
	m := &Manager{
		sessions: map[string]*Session{},
		words:    words,
		emit:     emit,
		store:    store,
	}
	if store != nil {
		if loaded, err := store.LoadAll(); err == nil {
			for i := range loaded {
				s := loaded[i]
				// A session persisted mid-run is not running any more.
				if s.State == StateRunning {
					s.State = StateStopped
				}
				s.emit = emit
				s.mu = &sync.Mutex{}
				m.sessions[s.ID] = &s
			}
		}
	}
	return m
}

// SetWords replaces the shared wordlist (e.g. after the user loads a file).
func (m *Manager) SetWords(words []string) {
	m.mu.Lock()
	m.words = words
	m.mu.Unlock()
}

// SetWordlistStore wires persistent wordlist storage and the embedded default.
// The built-in list becomes the initially active one.
func (m *Manager) SetWordlistStore(store *WordlistStore, builtin []string) {
	m.mu.Lock()
	m.wlStore = store
	m.builtin = builtin
	m.words = builtin
	m.activeWL = BuiltinWL
	m.mu.Unlock()
}

// Wordlists lists the built-in list plus every stored one, flagging the active.
func (m *Manager) Wordlists() []WLInfo {
	m.mu.Lock()
	active := m.activeWL
	builtinCount := len(m.builtin)
	store := m.wlStore
	m.mu.Unlock()

	out := []WLInfo{{Name: BuiltinWL, Count: builtinCount, Builtin: true, Active: active == BuiltinWL}}
	if store != nil {
		for _, wl := range store.List() {
			wl.Active = wl.Name == active
			out = append(out, wl)
		}
	}
	return out
}

// ActiveWordlist returns the name of the active wordlist.
func (m *Manager) ActiveWordlist() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeWL
}

// SelectWordlist loads a wordlist into RAM, replacing the current one. It
// emits a loading event before the (possibly slow) load and a ready event
// after, so the UI can show progress.
func (m *Manager) SelectWordlist(name string) (WLInfo, error) {
	m.emit.Emit(EvWLLoading, map[string]any{"name": name})

	var words []string
	builtin := name == BuiltinWL
	if builtin {
		m.mu.Lock()
		words = m.builtin
		m.mu.Unlock()
	} else {
		m.mu.Lock()
		store := m.wlStore
		m.mu.Unlock()
		if store == nil {
			return WLInfo{}, errors.New("no wordlist store")
		}
		w, err := store.Load(name)
		if err != nil {
			return WLInfo{}, err
		}
		words = w
	}

	// Swap the old list out of RAM for the new one.
	m.mu.Lock()
	m.words = words
	m.activeWL = name
	m.mu.Unlock()

	info := WLInfo{Name: name, Count: len(words), Builtin: builtin, Active: true}
	m.emit.Emit(EvWLReady, map[string]any{"name": name, "count": len(words)})
	return info, nil
}

// ImportWordlist copies an external file into the store and selects it.
func (m *Manager) ImportWordlist(srcPath string) (WLInfo, error) {
	m.mu.Lock()
	store := m.wlStore
	m.mu.Unlock()
	if store == nil {
		return WLInfo{}, errors.New("no wordlist store")
	}
	info, err := store.Import(srcPath)
	if err != nil {
		return WLInfo{}, err
	}
	return m.SelectWordlist(info.Name)
}

// AppendWords adds new words to the active list (deduplicated) and writes them
// back to the active source file. Appending to the built-in list creates a
// writable copy named "custom" and switches to it.
func (m *Manager) AppendWords(text string) (WLInfo, error) {
	add := ParseWordlist(text)
	if len(add) == 0 {
		return m.activeInfo(), nil
	}

	m.mu.Lock()
	existing := map[string]bool{}
	for _, w := range m.words {
		existing[w] = true
	}
	merged := append([]string(nil), m.words...)
	for _, w := range add {
		if !existing[w] {
			existing[w] = true
			merged = append(merged, w)
		}
	}
	m.words = merged
	active := m.activeWL
	store := m.wlStore
	if active == BuiltinWL {
		active = "custom"
		m.activeWL = active
	}
	m.mu.Unlock()

	if store != nil {
		if err := store.Save(active, merged); err != nil {
			return WLInfo{}, err
		}
	}
	info := WLInfo{Name: active, Count: len(merged), Builtin: false, Active: true}
	m.emit.Emit(EvWLReady, map[string]any{"name": active, "count": len(merged)})
	return info, nil
}

// DeleteWordlist removes a stored wordlist; falls back to built-in if it was
// active. The built-in list cannot be deleted.
func (m *Manager) DeleteWordlist(name string) error {
	if name == BuiltinWL {
		return errors.New("cannot delete built-in wordlist")
	}
	m.mu.Lock()
	store := m.wlStore
	wasActive := m.activeWL == name
	m.mu.Unlock()
	if store != nil {
		if err := store.Delete(name); err != nil {
			return err
		}
	}
	if wasActive {
		_, _ = m.SelectWordlist(BuiltinWL)
	}
	return nil
}

func (m *Manager) activeInfo() WLInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return WLInfo{Name: m.activeWL, Count: len(m.words), Builtin: m.activeWL == BuiltinWL, Active: true}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create registers a new session for baseURL.
func (m *Manager) Create(name, baseURL string, p ScanParams) (Session, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return Session{}, errors.New("empty url")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	p = p.withDefaults()
	if name == "" {
		name = baseURL
	}
	s := &Session{
		ID:        newID(),
		Name:      name,
		BaseURL:   strings.TrimRight(baseURL, "/"),
		State:     StateIdle,
		CreatedAt: time.Now(),
		MaxDepth:  p.MaxDepth,
		Recurse:   p.Recurse,
		emit:      m.emit,
		mu:        &sync.Mutex{},
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
	m.persist(s)
	return s.snapshot(), nil
}

// List returns all sessions, newest first.
func (m *Manager) List() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Get returns one session snapshot.
func (m *Manager) Get(id string) (Session, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return Session{}, ErrNotFound
	}
	return s.snapshot(), nil
}

func (m *Manager) get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Start begins (or restarts) the scan for a session. It clears previous hits.
func (m *Manager) Start(id string, p ScanParams) error {
	s, ok := m.get(id)
	if !ok {
		return ErrNotFound
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrRunning
	}
	p = p.withDefaults()
	s.running = true
	s.Hits = nil
	s.Endpoints = nil
	s.Err = ""
	s.MaxDepth = p.MaxDepth
	s.Recurse = p.Recurse
	s.Mode = p.Mode
	baseURL := s.BaseURL
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	transport := engine.TransportConfig{
		Timeout:       time.Duration(p.Timeout) * time.Second,
		MaxConns:      p.MaxConc,
		SkipTLSVerify: p.SkipTLS,
		ForceHTTP2:    true,
	}

	m.mu.Lock()
	words := m.words
	m.mu.Unlock()

	cfg := engine.Config{
		BaseURL:   baseURL,
		Words:     words,
		Adaptive:  engine.AdaptiveConfig{Min: 1, Max: p.MaxConc, Start: p.StartConc, Batch: 30},
		Transport: transport,
		Recurse:   p.Recurse,
		MaxDepth:  p.MaxDepth,
		Retries:   p.Retries,
	}

	s.startTimer()
	s.setState(StateRunning, "")

	doer := m.testDoer
	bodyDoer := m.testBody

	go func() {
		// Detect protocol first (best effort), unless a test doer is injected,
		// then build the transport best suited to it (h1 => fasthttp,
		// h2 => net/http multiplexing) via the injected factory.
		proto := engine.ProtocolUnknown
		if doer == nil {
			if p2, err := engine.DetectProtocol(ctx, baseURL, transport); err == nil {
				proto = p2
				s.mu.Lock()
				s.Protocol = proto.String()
				s.mu.Unlock()
			}
			if m.newDoer != nil {
				doer = m.newDoer(proto, transport)
			}
		}

		var runErr error

		// ---- discovery phase (ModeDiscover / ModeBoth) ----
		if p.Mode == ModeDiscover || p.Mode == ModeBoth {
			s.emitPhase("discovery")
			opts := discover.Options{
				BaseURL:   baseURL,
				Crawl:     p.Crawl,
				MineJS:    p.MineJS,
				UseRobots: p.UseRobots,
				MaxDepth:  p.CrawlDepth,
				MaxPages:  p.MaxPages,
				Timeout:   p.Timeout,
				SkipTLS:   p.SkipTLS,
				OnFound:   s.addEndpoint,
			}
			eps, err := discover.Discover(ctx, opts, bodyDoer)
			if err == nil {
				// Seed the brute-force from every discovered path.
				seeds := make([]string, 0, len(eps))
				for _, e := range eps {
					seeds = append(seeds, e.Path)
				}
				cfg.SeedDirs = seeds
			}
			runErr = ctx.Err()
		}

		// ---- brute-force phase (ModeBruteforce / ModeBoth) ----
		if runErr == nil && (p.Mode == ModeBruteforce || p.Mode == ModeBoth) {
			s.emitPhase("scan")
			runner := engine.NewRunner(cfg, s, doer)
			runErr = runner.Run(ctx)
		}

		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.mu.Unlock()
		s.stopTimer()

		switch {
		case errors.Is(runErr, context.Canceled):
			s.setState(StateStopped, "")
		case runErr != nil:
			s.setState(StateError, runErr.Error())
		default:
			s.setState(StateDone, "")
		}
		m.persist(s)
	}()

	return nil
}

// Stop cancels a running scan.
func (m *Manager) Stop(id string) error {
	s, ok := m.get(id)
	if !ok {
		return ErrNotFound
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Delete stops and removes a session.
func (m *Manager) Delete(id string) error {
	s, ok := m.get(id)
	if !ok {
		return ErrNotFound
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()

	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()

	if m.store != nil {
		_ = m.store.Delete(id)
	}
	return nil
}

func (m *Manager) persist(s *Session) {
	if m.store == nil {
		return
	}
	_ = m.store.Save(s.snapshot())
}
