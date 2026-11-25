package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFileLinkAndLoadPreview(t *testing.T) {
	// 1. Parse file:// with #L3-L6 range.
	p, s, e, ok := parseFileLink("file:///workspace/demo-app/src/handler.go#L3-L6")
	if !ok || p != "/workspace/demo-app/src/handler.go" || s != 3 || e != 6 {
		t.Fatalf("parse #L3-L6 mismatch: ok=%v p=%q s=%d e=%d", ok, p, s, e)
	}

	// 2. Parse file:// with single #L42 and URL-encoded Chinese filename.
	p2, s2, e2, ok2 := parseFileLink("file:///tmp/%E6%96%87%E6%A1%A3.md#L42")
	if !ok2 || p2 != "/tmp/文档.md" || s2 != 42 || e2 != 42 {
		t.Fatalf("parse encoded Chinese mismatch: ok=%v p=%q s=%d e=%d", ok2, p2, s2, e2)
	}

	// 3. Reject non-file URLs.
	if _, _, _, ok := parseFileLink("https://example.com/a"); ok {
		t.Fatalf("expected https URL not to parse as file link")
	}

	// 4. Load real local file preview with highlighted line range.
	dir := t.TempDir()
	fp := filepath.Join(dir, "sample.xml")
	_ = os.WriteFile(fp, []byte("<mapper>\n  <select id=\"a\">\n    SELECT 1\n  </select>\n</mapper>\n"), 0o644)

	data, err := loadFilePreview(fp, 2, 3, 80)
	if err != nil {
		t.Fatalf("loadFilePreview failed: %v", err)
	}
	if data.TotalLines < 5 || !strings.Contains(data.Content, "▸") || !strings.Contains(data.Content, "SELECT 1") {
		t.Fatalf("unexpected preview content:\n%s", data.Content)
	}
}
