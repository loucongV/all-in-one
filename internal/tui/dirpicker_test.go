package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirPickerNavigationAndGitTag(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	beta := filepath.Join(root, "beta")
	hidden := filepath.Join(root, ".secret")
	_ = os.MkdirAll(filepath.Join(alpha, ".git"), 0o755)
	_ = os.MkdirAll(beta, 0o755)
	_ = os.MkdirAll(hidden, 0o755)

	added := map[string]bool{beta: true}
	p := newDirPicker(root, added)

	// Should have: ".", "..", "alpha", "beta" (hidden skipped by default).
	if len(p.entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(p.entries), p.entries)
	}
	if p.entries[0].kind != dirEntryCurrent || p.entries[1].kind != dirEntryParent {
		t.Fatalf("unexpected header entries: %+v", p.entries[:2])
	}
	if p.entries[2].name != "alpha" || !p.entries[2].isGit {
		t.Fatalf("expected alpha with isGit=true, got %+v", p.entries[2])
	}
	if p.entries[3].name != "beta" || !p.entries[3].isAdded {
		t.Fatalf("expected beta with isAdded=true, got %+v", p.entries[3])
	}

	// Move to alpha and enter it, then go back up and verify cursor returns to alpha.
	p.moveDown() // ..
	p.moveDown() // alpha
	p.enterSelectedDir(added)
	if p.currentDir != alpha {
		t.Fatalf("expected currentDir=%s, got %s", alpha, p.currentDir)
	}
	p.goUp(added)
	if p.currentDir != root {
		t.Fatalf("expected currentDir=%s, got %s", root, p.currentDir)
	}
	sel, ok := p.selected()
	if !ok || sel.name != "alpha" {
		t.Fatalf("expected cursor restored to alpha, got %+v", sel)
	}

	// Toggle hidden dirs should reveal .secret.
	p.toggleHidden(added)
	foundHidden := false
	for _, e := range p.entries {
		if e.name == ".secret" {
			foundHidden = true
		}
	}
	if !foundHidden {
		t.Fatalf("expected .secret visible after toggleHidden")
	}
}
