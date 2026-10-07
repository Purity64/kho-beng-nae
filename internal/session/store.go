package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileStore persists each session as a JSON file under Dir.
type FileStore struct {
	Dir string
	mu  sync.Mutex
}

// NewFileStore creates the directory if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{Dir: dir}, nil
}

func (f *FileStore) path(id string) string {
	// ids are hex, but guard anyway.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(f.Dir, "session-"+safe+".json")
}

// Save writes one session atomically (temp file + rename).
func (f *FileStore) Save(s Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path(s.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.path(s.ID))
}

// LoadAll reads every session file in Dir.
func (f *FileStore) LoadAll() ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "session-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.Dir, e.Name()))
		if err != nil {
			continue
		}
		var s Session
		if json.Unmarshal(data, &s) == nil && s.ID != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// Delete removes a session file.
func (f *FileStore) Delete(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := os.Remove(f.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
