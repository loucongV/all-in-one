package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Build-time metadata, injectable via -ldflags:
//
//	-X github.com/loucongV/all-in-one/internal/version.Version=v0.1.0
//	-X github.com/loucongV/all-in-one/internal/version.Commit=abc1234
//	-X github.com/loucongV/all-in-one/internal/version.BuildTime=2026-11-25T10:30:00+08:00
var (
	Version   = "v0.1.0"
	Commit    = ""
	BuildTime = ""
)

// Short returns the concise semantic version label (e.g. "v0.1.0").
func Short() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		return "dev"
	}
	return v
}

// ResolvedCommit returns the injected commit hash or falls back to Go build VCS info.
func ResolvedCommit() string {
	if c := strings.TrimSpace(Commit); c != "" {
		return c
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev string
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 7 {
				rev = rev[:7]
			}
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
	}
	return "dev"
}

// ResolvedBuildTime returns the injected build timestamp or falls back to Go build VCS time.
func ResolvedBuildTime() string {
	if t := strings.TrimSpace(BuildTime); t != "" {
		return t
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" && strings.TrimSpace(s.Value) != "" {
				return s.Value
			}
		}
	}
	return ""
}

// Full returns a single-line detailed version string for `aio --version` / `aio version`.
func Full() string {
	parts := []string{"commit " + ResolvedCommit()}
	if bt := ResolvedBuildTime(); bt != "" {
		parts = append(parts, "built "+bt)
	}
	parts = append(parts, fmt.Sprintf("%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH))
	return fmt.Sprintf("aio %s (%s)", Short(), strings.Join(parts, ", "))
}
