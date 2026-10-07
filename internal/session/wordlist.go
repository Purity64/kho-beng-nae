package session

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WLInfo describes a wordlist for the UI.
type WLInfo struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Builtin bool   `json:"builtin"`
	Active  bool   `json:"active"`
}

// WordlistStore persists uploaded wordlists as .txt files under Dir. The
// built-in list is not stored here; it lives embedded in the binary.
type WordlistStore struct {
	Dir string
}

// NewWordlistStore creates the directory if needed.
func NewWordlistStore(dir string) (*WordlistStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &WordlistStore{Dir: dir}, nil
}

func safeName(name string) string {
	name = strings.TrimSuffix(filepath.Base(name), ".txt")
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
	if name == "" {
		name = "wordlist"
	}
	return name
}

func (w *WordlistStore) path(name string) string {
	return filepath.Join(w.Dir, safeName(name)+".txt")
}

// List returns the stored wordlists (name + count).
func (w *WordlistStore) List() []WLInfo {
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		return nil
	}
	var out []WLInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		words, _ := w.Load(name)
		out = append(out, WLInfo{Name: name, Count: len(words)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Load reads a stored wordlist into a cleaned slice.
func (w *WordlistStore) Load(name string) ([]string, error) {
	data, err := os.ReadFile(w.path(name))
	if err != nil {
		return nil, err
	}
	return ParseWordlist(string(data)), nil
}

// Import copies an external file into the store and returns its info.
func (w *WordlistStore) Import(srcPath string) (WLInfo, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return WLInfo{}, err
	}
	name := safeName(filepath.Base(srcPath))
	if err := os.WriteFile(w.path(name), data, 0o644); err != nil {
		return WLInfo{}, err
	}
	words := ParseWordlist(string(data))
	return WLInfo{Name: name, Count: len(words)}, nil
}

// Save overwrites a stored wordlist with the given words (one per line).
func (w *WordlistStore) Save(name string, words []string) error {
	var b strings.Builder
	for _, x := range words {
		b.WriteString(x)
		b.WriteByte('\n')
	}
	return os.WriteFile(w.path(name), []byte(b.String()), 0o644)
}

// Delete removes a stored wordlist.
func (w *WordlistStore) Delete(name string) error {
	err := os.Remove(w.path(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ParseWordlist splits raw text into cleaned, comment-free lines.
func ParseWordlist(data string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
