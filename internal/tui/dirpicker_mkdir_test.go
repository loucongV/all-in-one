package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirPickerCreateSubDirAndRejectTraversal(t *testing.T) {
	root := t.TempDir()
	p := newDirPicker(root, nil)

	// Reject traversal & absolute paths.
	for _, bad := range []string{"", "..", "../outside", "/tmp/abs", "a/../../outside"} {
		if _, err := p.createSubDir(bad, false, nil); err == nil {
			t.Fatalf("expected createSubDir(%q) to fail", bad)
		}
	}

	// Create nested directory with git init and verify it is selected.
	created, err := p.createSubDir("services/demo-app", true, nil)
	if err != nil {
		t.Fatalf("createSubDir failed: %v", err)
	}
	wantPath := filepath.Join(root, "services", "demo-app")
	if created != wantPath {
		t.Fatalf("created = %q, want %q", created, wantPath)
	}
	if fi, err := os.Stat(filepath.Join(wantPath, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf("expected git init to create .git directory")
	}
	sel, ok := p.selected()
	if !ok || sel.path != wantPath || !sel.isGit {
		t.Fatalf("expected newly created git folder to be selected, got (%+v, %v)", sel, ok)
	}
}
