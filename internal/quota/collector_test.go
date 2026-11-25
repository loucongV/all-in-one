package quota

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectorCodexStatusAndUsageScreenCapture(t *testing.T) {
	fakeHome := t.TempDir()
	aioHome := t.TempDir()

	// 1. Seed a Codex rollout file with official /status rate_limits.
	codexDir := filepath.Join(fakeHome, ".codex", "sessions", "2026", "11", "23")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resetTS := time.Now().Add(50 * time.Hour).Unix()
	codexLine := fmt.Sprintf(`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":85.0,"window_minutes":10080,"resets_at":%d},"credits":{"has_credits":true,"balance":"120.50"},"plan_type":"prolite"}}}`, resetTS)
	if err := os.WriteFile(filepath.Join(codexDir, "rollout-test.jsonl"), []byte(codexLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := NewCollectorWithHome(fakeHome, aioHome, nil, nil)
	snap := c.Snapshot()
	byAgent := func(name string) int {
		for i, q := range snap {
			if q.Agent == name {
				return i
			}
		}
		t.Fatalf("missing agent %s", name)
		return -1
	}

	codexQ := snap[byAgent("codex")]
	if codexQ.State != "low" || codexQ.RemainingPct == nil || *codexQ.RemainingPct != 15.0 {
		t.Fatalf("expected codex state=low remaining=15%%, got %+v", codexQ)
	}
	if !strings.Contains(codexQ.Detail, "Credits: 120") {
		t.Fatalf("expected codex detail with credits, got %q", codexQ.Detail)
	}

	// 2. Simulate running /usage inside Claude Code (Pro/Max style: % used).
	claudeUsageScreen := `
  /usage
  Current session
  ████████░░░░░░░░  38% used   Resets in 2h 15m
  Current week (all models)
  ████████████████  82% used   Resets Nov 28
`
	if !c.IngestScreen("claude", claudeUsageScreen) {
		t.Fatalf("expected IngestScreen to parse Claude /usage output")
	}
	snap = c.Snapshot()
	claudeQ := snap[byAgent("claude")]
	if claudeQ.RemainingPct == nil || *claudeQ.RemainingPct != 18.0 || claudeQ.State != "low" {
		t.Fatalf("expected claude remaining=18%% (most constrained window) state=low, got %+v", claudeQ)
	}

	// 3. Simulate Cursor /usage (request ratio).
	cursorUsageScreen := `
  Plan Usage
  Fast requests used: 350 / 500 requests
  Resets in 12 days
`
	if !c.IngestScreen("cursor", cursorUsageScreen) {
		t.Fatalf("expected IngestScreen to parse Cursor /usage ratio")
	}
	snap = c.Snapshot()
	cursorQ := snap[byAgent("cursor")]
	if cursorQ.RemainingPct == nil || *cursorQ.RemainingPct != 30.0 || cursorQ.State != "ok" {
		t.Fatalf("expected cursor remaining=30%% state=ok, got %+v", cursorQ)
	}

	// 4. Simulate real Antigravity /usage ("Models & Quota" multi-line output).
	agyUsageScreen := `
└ Models & Quota
  Account: test@example.com

GEMINI MODELS
  Weekly Limit Remaining
    [██████████████████████████████░░░░░░░░░░░░░░░░░░░░] 59.77%
    Refreshes in 144h 11m

  Five Hour Limit Remaining
    [█████████████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░] 42.01%
    Refreshes in 2h 17m

CLAUDE AND GPT MODELS
  Weekly Limit Remaining
    [██████████████████████████████████████████████████] 100.00%
    Quota available
`
	if !c.IngestScreen("agy", agyUsageScreen) {
		t.Fatalf("expected IngestScreen to parse Antigravity Models & Quota")
	}
	snap = c.Snapshot()
	agyQ := snap[byAgent("agy")]
	if agyQ.RemainingPct == nil || *agyQ.RemainingPct != 42.0 || agyQ.Summary != "G:42% C:100%" {
		t.Fatalf("expected agy multi-pool summary 'G:42%% C:100%%' with rem=42%%, got %+v", agyQ)
	}
	if !strings.Contains(agyQ.Detail, "Gemini 5h: 42% 剩 (刷新 2h17m)") {
		t.Fatalf("expected agy detail to keep refresh window, got:\n%s", agyQ.Detail)
	}

	// 4b. Verify Claude Code API Usage Billing /usage output ("Estimated cost: $0.0000").
	claudeAPIScreen := `
  Settings  Status   Config   Usage   Stats
  Session
  Estimated cost:         $0.1250
  Usage:                  100 input, 20 output
`
	if q, ok := ParseUsageScreen("claude", claudeAPIScreen); !ok || !strings.Contains(q.Summary, "$0.1250") {
		t.Fatalf("expected Claude API billing /usage parse, got ok=%v q=%+v", ok, q)
	}

	// 5. Verify cache persistence across Collector restart!
	c2 := NewCollectorWithHome(fakeHome, aioHome, nil, nil)
	snap2 := c2.Snapshot()
	if snap2[byAgent("claude")].RemainingPct == nil || *snap2[byAgent("claude")].RemainingPct != 18.0 {
		t.Fatalf("expected persisted claude /usage quota across restart, got %+v", snap2[byAgent("claude")])
	}
}

func TestCollectorActiveAgentsAndKimiOpenCodeSupport(t *testing.T) {
	c := NewCollectorWithHome(t.TempDir(), t.TempDir(), nil, nil)
	c.SetActiveAgentsFunc(func() []string {
		return []string{"codex", "kimi", "opencode"}
	})
	snap := c.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 active agent quotas, got %d (%+v)", len(snap), snap)
	}
	if snap[0].Agent != "codex" || snap[1].Agent != "kimi" || snap[2].Agent != "opencode" {
		t.Fatalf("unexpected active quota order: %+v", snap)
	}
	if snap[1].State != "ok" || !strings.Contains(snap[1].Summary, "正常") {
		t.Fatalf("expected kimi default quota state=ok, got %+v", snap[1])
	}

	// Rate-limiting kimi overlays limited state immediately.
	c.MarkRateLimited("kimi", "rate limit exceeded")
	snap = c.Snapshot()
	if snap[1].State != "limited" {
		t.Fatalf("expected kimi limited after MarkRateLimited, got %+v", snap[1])
	}
}

func TestParseCursorUsageTableAndCompactAgyRefresh(t *testing.T) {
	cursorScreen := `
Usage · Super
Monthly plan and on-demand usage

Category        Current          Usage
Included        1% used          █░░░░░░░░░░░
  Auto          1% used          █░░░░░░░░░░░
  API           0% used          ░░░░░░░░░░░░
On-Demand       $0               ────────────

No monthly limit

View in dashboard: cursor.com/dashboard?tab=usage
`
	q, ok := ParseUsageScreen("cursor", cursorScreen)
	if !ok {
		t.Fatalf("expected Cursor /usage report to parse")
	}
	if q.State != "ok" || q.RemainingPct == nil || *q.RemainingPct != 99.0 {
		t.Fatalf("expected cursor 99%% remaining state=ok, got %+v", q)
	}
	if q.Summary != "99% 剩 · Super" {
		t.Fatalf("expected summary '99%% 剩 · Super', got %q", q.Summary)
	}
	if !strings.Contains(q.Detail, "Auto: 1% used") || !strings.Contains(q.Detail, "On-Demand: $0") {
		t.Fatalf("expected detailed breakdown in Detail, got:\n%s", q.Detail)
	}

	if got := compactRefreshDuration("154h 40m"); got != "6d10h" {
		t.Fatalf("expected 154h 40m -> 6d10h, got %q", got)
	}
	if got := compactRefreshDuration("3h 39m"); got != "3h39m" {
		t.Fatalf("expected 3h 39m -> 3h39m, got %q", got)
	}

	// Verify Agy partial pool exhaustion (Claude/GPT 5h = 0% while Gemini = 44%) shows amber G:44% C:0% (NOT red limited!).
	partialAgy := `
└ Models & Quota
GEMINI MODELS
  Weekly Limit Remaining
    [██████████████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░] 44.00%
    Refreshes in 153h 0m
  Five Hour Limit Remaining
    [██████████████████████████░░░░░░░░░░░░░░░░░░░░░░░░] 53.00%
    Refreshes in 2h 46m
CLAUDE AND GPT MODELS
  Weekly Limit Remaining
    [███████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░] 23.00%
    Refreshes in 167h 0m
  Five Hour Limit Remaining
    [░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░] 0.00%
    Refreshes in 4h 32m
`
	qAgy, ok := ParseUsageScreen("agy", partialAgy)
	if !ok {
		t.Fatalf("expected partial agy quota to parse")
	}
	if qAgy.State != "low" || qAgy.RemainingPct == nil || *qAgy.RemainingPct != 44.0 || qAgy.Summary != "G:44% C:0%" {
		t.Fatalf("expected partial agy state=low rem=44%% summary='G:44%% C:0%%', got %+v", qAgy)
	}
}

func TestAuthoritativeQuotaOverridesTextRateLimitDetection(t *testing.T) {
	// Custom command disables the silent verification probe so the test stays hermetic.
	c := NewCollectorWithHome(t.TempDir(), t.TempDir(), map[string]string{"agy": "true"}, nil)
	agyScreen := `
└ Models & Quota
GEMINI MODELS
  Weekly Limit Remaining
    [██████████████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░] 46.00%
    Refreshes in 154h 40m
`
	if !c.IngestScreen("agy", agyScreen) {
		t.Fatalf("expected agy usage to parse")
	}
	// Chat transcript mentions RESOURCE_EXHAUSTED -> must NOT turn agy red while 46% remains.
	c.MarkRateLimited("agy", "RESOURCE_EXHAUSTED")
	for _, q := range c.Snapshot() {
		if q.Agent == "agy" && (q.State == "limited" || strings.HasPrefix(q.Summary, "限额")) {
			t.Fatalf("expected authoritative 46%% remaining to override text detection, got %+v", q)
		}
	}

	// Without authoritative data (e.g. kimi), a detected limit is still surfaced.
	c.MarkRateLimited("kimi", "429 Too Many Requests")
	found := false
	for _, q := range c.SnapshotFor([]string{"kimi"}) {
		if q.Agent == "kimi" && q.State == "limited" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected kimi to be marked limited without authoritative quota data")
	}
}

func TestDetectRateLimitIgnoresSourceCodeAndDiffs(t *testing.T) {
	// Genuine CLI errors MUST still be detected.
	for _, realErr := range []string{
		"Error: 429 Too Many Requests",
		"You've hit your 5-hour limit. Please try again later.",
		"API Error: rate limit exceeded for model",
		"RESOURCE_EXHAUSTED: quota exceeded",
		"Usage limit reached for this billing period",
	} {
		if _, hit := DetectRateLimit([]byte(realErr)); !hit {
			t.Fatalf("expected real rate-limit error to be detected: %q", realErr)
		}
	}

	// Code, diffs, comments, and prose MUST NOT false-trigger red limited state.
	for _, falsePos := range []string{
		`var rateLimitRe = regexp.MustCompile("rate limit exceeded")`,
		`+ c.MarkRateLimited("kimi", "rate limit exceeded")`,
		`// Real-time PTY rate-limit detection for quota exceeded`,
		`425: if detail, hit := quota.DetectRateLimit(chunk); hit {`,
		`  "detail": "rate limit exceeded",`,
		`This test checks whether rate limit exceeded is handled.`,
	} {
		if loc, hit := DetectRateLimit([]byte(falsePos)); hit {
			t.Fatalf("unexpected false-positive rate limit on %q (matched %q)", falsePos, loc)
		}
	}
}
