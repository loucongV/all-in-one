package version

import (
	"strings"
	"testing"
)

func TestVersionStrings(t *testing.T) {
	if s := Short(); s == "" {
		t.Fatalf("expected non-empty Short() version")
	}
	full := Full()
	if !strings.HasPrefix(full, "aio ") || !strings.Contains(full, Short()) || !strings.Contains(full, "commit ") {
		t.Fatalf("unexpected Full() output: %q", full)
	}
}
