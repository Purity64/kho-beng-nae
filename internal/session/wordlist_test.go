package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWordlistManagement(t *testing.T) {
	dir := t.TempDir()
	store, err := NewWordlistStore(filepath.Join(dir, "wl"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, NopEmitter{}, nil)
	m.SetWordlistStore(store, []string{"admin", "login"})

	// built-in active
	if m.ActiveWordlist() != BuiltinWL {
		t.Fatalf("active = %q, want built-in", m.ActiveWordlist())
	}
	wls := m.Wordlists()
	if len(wls) != 1 || !wls[0].Builtin || wls[0].Count != 2 {
		t.Fatalf("built-in listing wrong: %+v", wls)
	}

	// import a file and auto-select it
	src := filepath.Join(dir, "big.txt")
	os.WriteFile(src, []byte("a\nb\nc\n# comment\n\n"), 0o644)
	info, err := m.ImportWordlist(src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Count != 3 || m.ActiveWordlist() != "big" {
		t.Fatalf("import wrong: %+v active=%s", info, m.ActiveWordlist())
	}

	// append words -> dedup + persisted to the active file
	info, err = m.AppendWords("c\nd\ne")
	if err != nil {
		t.Fatal(err)
	}
	if info.Count != 5 { // a,b,c + d,e ("c" deduped)
		t.Errorf("after append want 5, got %d", info.Count)
	}
	// file on disk reflects it
	reloaded, _ := store.Load("big")
	if len(reloaded) != 5 {
		t.Errorf("disk file has %d words, want 5", len(reloaded))
	}

	// back to built-in, then append -> creates "custom"
	if _, err := m.SelectWordlist(BuiltinWL); err != nil {
		t.Fatal(err)
	}
	info, _ = m.AppendWords("x")
	if info.Name != "custom" || m.ActiveWordlist() != "custom" {
		t.Errorf("append-on-builtin should switch to custom, got %+v", info)
	}
	if info.Count != 3 { // admin, login + x
		t.Errorf("custom count = %d, want 3", info.Count)
	}

	// delete active custom -> falls back to built-in
	if err := m.DeleteWordlist("custom"); err != nil {
		t.Fatal(err)
	}
	if m.ActiveWordlist() != BuiltinWL {
		t.Errorf("after deleting active, want built-in, got %s", m.ActiveWordlist())
	}
	if err := m.DeleteWordlist(BuiltinWL); err == nil {
		t.Errorf("deleting built-in should error")
	}
}
