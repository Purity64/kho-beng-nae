package main

import (
	"bufio"
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"Kho-beng-nae/internal/session"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// defaultWordlist is compiled into the binary so the app always has a list to
// scan with, even before the user loads their own.
//
//go:embed wordlist/wordlist.txt
var defaultWordlist string

// wailsEmitter adapts the Wails runtime event bus to session.Emitter.
type wailsEmitter struct{ ctx context.Context }

func (e *wailsEmitter) Emit(event string, data any) {
	if e.ctx != nil {
		wruntime.EventsEmit(e.ctx, event, data)
	}
}

// App is the object bound to the frontend. Every exported method is callable
// from JavaScript as window.go.main.App.<Method>().
type App struct {
	ctx     context.Context
	mgr     *session.Manager
	emitter *wailsEmitter
}

// NewApp constructs the application.
func NewApp() *App {
	return &App{emitter: &wailsEmitter{}}
}

// startup is called by Wails once the runtime is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.emitter.ctx = ctx

	words := parseWordlist(defaultWordlist)

	store, err := session.NewFileStore(subDir("sessions"))
	if err != nil {
		store = nil // fall back to in-memory only
	}
	a.mgr = session.NewManager(words, a.emitter, store)
	// Pick fasthttp for HTTP/1 targets, net/http (h2 multiplexing) for HTTP/2.
	a.mgr.SetDoerFactory(pickDoer)

	// Persistent wordlist library (built-in + user-uploaded lists).
	if wls, err := session.NewWordlistStore(subDir("wordlists")); err == nil {
		a.mgr.SetWordlistStore(wls, words)
	}
}

// ---- bound API ------------------------------------------------------------

// CreateSession registers a new target session.
func (a *App) CreateSession(name, url string, params session.ScanParams) (session.Session, error) {
	return a.mgr.Create(name, url, params)
}

// ListSessions returns all sessions, newest first.
func (a *App) ListSessions() []session.Session {
	return a.mgr.List()
}

// GetSession returns one session with its hits.
func (a *App) GetSession(id string) (session.Session, error) {
	return a.mgr.Get(id)
}

// StartSession begins or restarts a scan.
func (a *App) StartSession(id string, params session.ScanParams) error {
	return a.mgr.Start(id, params)
}

// StopSession cancels a running scan.
func (a *App) StopSession(id string) error {
	return a.mgr.Stop(id)
}

// DeleteSession removes a session.
func (a *App) DeleteSession(id string) error {
	return a.mgr.Delete(id)
}

// WordlistInfo reports the active wordlist.
func (a *App) WordlistInfo() map[string]any {
	for _, wl := range a.mgr.Wordlists() {
		if wl.Active {
			return map[string]any{"name": wl.Name, "count": wl.Count}
		}
	}
	return map[string]any{"name": session.BuiltinWL, "count": 0}
}

// ListWordlists returns the built-in list plus all stored lists.
func (a *App) ListWordlists() []session.WLInfo {
	return a.mgr.Wordlists()
}

// SelectWordlist swaps the active wordlist in RAM.
func (a *App) SelectWordlist(name string) (session.WLInfo, error) {
	return a.mgr.SelectWordlist(name)
}

// AppendWords adds typed words to the active list and saves them.
func (a *App) AppendWords(text string) (session.WLInfo, error) {
	return a.mgr.AppendWords(text)
}

// DeleteWordlist removes a stored wordlist.
func (a *App) DeleteWordlist(name string) error {
	return a.mgr.DeleteWordlist(name)
}

// PickWordlist opens a file dialog, imports the chosen file into the store and
// selects it.
func (a *App) PickWordlist() (session.WLInfo, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Choose a wordlist",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Text files", Pattern: "*.txt"},
			{DisplayName: "All files", Pattern: "*.*"},
		},
	})
	if err != nil || path == "" {
		return session.WLInfo{}, err
	}
	return a.mgr.ImportWordlist(path)
}

// OpenURL opens a discovered URL in the user's default browser.
func (a *App) OpenURL(url string) {
	wruntime.BrowserOpenURL(a.ctx, url)
}

// ---- helpers --------------------------------------------------------------

func parseWordlist(data string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func subDir(name string) string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base, _ = os.Getwd()
	}
	return filepath.Join(base, "kho-beng-nae", name)
}
