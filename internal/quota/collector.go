package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/loucongV/all-in-one/internal/protocol"
)

type rateLimitHit struct {
	detail string
	at     time.Time
}

// Collector gathers real /usage and /status quota metrics for Codex, Claude Code,
// Antigravity, and Cursor CLI via:
//  1. Native local status data (Codex rollout rate_limits -> exact /status data)
//  2. Passive & on-demand PTY /usage & /status screen capture (persisted in ~/.aio/quota_cache.json)
//  3. Optional user-configured [quota_commands] in ~/.aio/config.toml
//  4. Real-time PTY rate-limit detection
type Collector struct {
	userHome       string
	aioHome        string
	cachePath      string
	commands       map[string]string
	onChange       func()
	activeFn       func() []string
	order          []string
	mu             sync.RWMutex
	quotas         map[string]protocol.AgentQuota
	captured       map[string]protocol.AgentQuota // persisted /usage & /status captures
	limited        map[string]rateLimitHit
	cmdRunning     map[string]bool
	cmdAt          map[string]time.Time
	verifyAt       map[string]time.Time
	cursorBio      string
	cursorAt       time.Time
	cursorFetching bool
	probing        bool
}

func NewCollector(aioHome string, commands map[string]string, onChange func()) *Collector {
	home, _ := os.UserHomeDir()
	return NewCollectorWithHome(home, aioHome, commands, onChange)
}

func NewCollectorWithHome(userHome, aioHome string, commands map[string]string, onChange func()) *Collector {
	cachePath := ""
	if aioHome != "" {
		cachePath = filepath.Join(aioHome, "quota_cache.json")
	}
	c := &Collector{
		userHome:   userHome,
		aioHome:    aioHome,
		cachePath:  cachePath,
		commands:   commands,
		onChange:   onChange,
		order:      []string{"claude", "codex", "agy", "cursor", "kimi", "opencode"},
		quotas:     make(map[string]protocol.AgentQuota),
		captured:   make(map[string]protocol.AgentQuota),
		limited:    make(map[string]rateLimitHit),
		cmdRunning: make(map[string]bool),
		cmdAt:      make(map[string]time.Time),
	}
	c.loadCache()
	c.Refresh()
	return c
}

// SetActiveAgentsFunc registers a callback returning the currently active/opened agent names
// so background probes and default snapshots only track agents the user actually uses.
func (c *Collector) SetActiveAgentsFunc(fn func() []string) {
	c.mu.Lock()
	c.activeFn = fn
	c.mu.Unlock()
}

func (c *Collector) StartLoop(ctx context.Context) {
	// Kick off an initial silent background probe shortly after daemon startup.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
			c.ProbeAllSilent(ctx)
		}
	}()

	fastTicker := time.NewTicker(15 * time.Second)
	defer fastTicker.Stop()
	probeTicker := time.NewTicker(5 * time.Minute)
	defer probeTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-fastTicker.C:
			c.Refresh()
		case <-probeTicker.C:
			go c.ProbeAllSilent(ctx)
		}
	}
}

// ProbeAllSilent runs hidden ephemeral /usage probes for agy and claude (when no custom
// quota command overrides them), serialized one at a time to stay lightweight.
func (c *Collector) ProbeAllSilent(ctx context.Context) {
	c.mu.Lock()
	if c.probing {
		c.mu.Unlock()
		return
	}
	c.probing = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.probing = false
		c.mu.Unlock()
	}()

	c.mu.RLock()
	activeFn := c.activeFn
	c.mu.RUnlock()
	var activeSet map[string]bool
	if activeFn != nil {
		if list := activeFn(); len(list) > 0 {
			activeSet = make(map[string]bool, len(list))
			for _, ag := range list {
				activeSet[ag] = true
			}
		}
	}

	for _, agent := range []string{"agy", "claude", "cursor"} {
		if ctx.Err() != nil {
			return
		}
		if activeSet != nil && !activeSet[agent] {
			continue
		}
		if strings.TrimSpace(c.commands[agent]) != "" {
			continue
		}
		screen, err := ProbeAgentUsage(ctx, agent, c.aioHome)
		if err != nil || strings.TrimSpace(screen) == "" {
			continue
		}
		c.IngestScreen(agent, screen)
	}
}

func (c *Collector) RefreshAsync() {
	go c.Refresh()
}

func (c *Collector) Refresh() {
	// Trigger configured custom quota commands asynchronously (at most once per 60s).
	for agent, cmdStr := range c.commands {
		if strings.TrimSpace(cmdStr) == "" {
			continue
		}
		c.runCustomCommandAsync(agent, cmdStr)
	}

	codexQ := c.collectCodex()

	now := time.Now()
	c.mu.Lock()
	changed := false
	for _, agent := range c.order {
		var q protocol.AgentQuota
		if agent == "codex" && codexQ.State != "unknown" {
			q = codexQ
		} else if capQ, ok := c.captured[agent]; ok {
			q = capQ
		} else if agent == "codex" {
			q = codexQ
		} else if agent == "cursor" {
			q = c.collectCursorLocked()
		} else if agent == "claude" || agent == "agy" {
			q = protocol.AgentQuota{
				Agent:     agent,
				State:     "unknown",
				Summary:   "后台探测中...",
				UpdatedAt: now,
			}
		} else {
			q = protocol.AgentQuota{
				Agent:     agent,
				State:     "ok",
				Summary:   "正常 (监听限流)",
				Detail:    "已开启实时限流检测；在终端运行 /usage 或配置 [quota_commands] 可展示余量",
				UpdatedAt: now,
			}
		}

		// Overlay active PTY rate-limit detection (valid for 20 minutes unless cleared).
		if hit, ok := c.limited[agent]; ok {
			// An authoritative /usage or /status reading with quota left always wins over
			// text-based PTY detection (chat transcripts may legitimately mention
			// "RESOURCE_EXHAUSTED", transient model-capacity 429s, etc.). A real limit hit is
			// confirmed by the verification probe triggered from MarkRateLimited.
			if q.RemainingPct != nil && *q.RemainingPct > 0 {
				delete(c.limited, agent)
			} else if now.Sub(hit.at) < 20*time.Minute {
				q.State = "limited"
				if q.RemainingPct == nil {
					q.Summary = "限额 / 限流"
				} else if !strings.HasPrefix(q.Summary, "限额") {
					q.Summary = "限额! " + q.Summary
				}
				if q.Detail == "" {
					q.Detail = hit.detail
				} else if hit.detail != "" && !strings.Contains(q.Detail, hit.detail) {
					q.Detail += "\n限流提示: " + hit.detail
				}
			} else {
				delete(c.limited, agent)
			}
		}

		prev, exists := c.quotas[agent]
		if !exists || prev.State != q.State || prev.Summary != q.Summary || prev.Detail != q.Detail {
			changed = true
		}
		c.quotas[agent] = q
	}
	c.mu.Unlock()

	if changed && c.onChange != nil {
		c.onChange()
	}
}

func (c *Collector) Snapshot() []protocol.AgentQuota {
	c.mu.RLock()
	activeFn := c.activeFn
	c.mu.RUnlock()
	if activeFn != nil {
		return c.SnapshotFor(activeFn())
	}
	return c.SnapshotFor(c.order)
}

// SnapshotFor returns quota entries strictly for the given list of agent names (in order).
func (c *Collector) SnapshotFor(agents []string) []protocol.AgentQuota {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(agents) == 0 {
		return nil
	}
	now := time.Now()
	seen := make(map[string]bool, len(agents))
	out := make([]protocol.AgentQuota, 0, len(agents))
	for _, agent := range agents {
		if agent == "" || seen[agent] {
			continue
		}
		seen[agent] = true
		if q, ok := c.quotas[agent]; ok {
			out = append(out, q)
			continue
		}
		out = append(out, protocol.AgentQuota{
			Agent:     agent,
			State:     "ok",
			Summary:   "正常 (监听限流)",
			UpdatedAt: now,
		})
	}
	return out
}

func (c *Collector) MarkRateLimited(agent, detail string) {
	c.mu.Lock()
	c.limited[agent] = rateLimitHit{detail: detail, at: time.Now()}
	c.mu.Unlock()
	c.Refresh()
	c.verifyAsync(agent)
}

// verifyAsync re-probes /usage for agents that support silent probing so a suspected
// rate-limit is confirmed (0% left) or cleared (quota still available). Throttled per agent.
func (c *Collector) verifyAsync(agent string) {
	switch agent {
	case "agy", "claude", "cursor":
	default:
		return
	}
	if strings.TrimSpace(c.commands[agent]) != "" {
		return
	}
	c.mu.Lock()
	if c.verifyAt == nil {
		c.verifyAt = make(map[string]time.Time)
	}
	if time.Since(c.verifyAt[agent]) < 2*time.Minute {
		c.mu.Unlock()
		return
	}
	c.verifyAt[agent] = time.Now()
	c.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if screen, err := ProbeAgentUsage(ctx, agent, c.aioHome); err == nil && strings.TrimSpace(screen) != "" {
			c.IngestScreen(agent, screen)
		}
	}()
}

func (c *Collector) ClearRateLimit(agent string) {
	c.mu.Lock()
	_, existed := c.limited[agent]
	delete(c.limited, agent)
	c.mu.Unlock()
	if existed {
		c.Refresh()
	}
}

// IngestScreen parses rendered plain terminal text from a running Agent PTY.
// Whenever the user runs /usage or /status inside Claude, Codex, Antigravity, or Cursor,
// this automatically captures the official quota metrics into the live dashboard.
func (c *Collector) IngestScreen(agent, plainScreen string) bool {
	q, ok := ParseUsageScreen(agent, plainScreen)
	if !ok {
		return false
	}
	c.mu.Lock()
	prev, exists := c.captured[agent]
	c.captured[agent] = q
	// Clear any stale rate-limit override if freshly parsed /usage shows >0% remaining.
	if q.RemainingPct != nil && *q.RemainingPct > 0 {
		delete(c.limited, agent)
	}
	changed := !exists || prev.Summary != q.Summary || prev.State != q.State || prev.Detail != q.Detail
	c.mu.Unlock()

	if changed {
		c.saveCache()
		c.Refresh()
	}
	return true
}

func (c *Collector) runCustomCommandAsync(agent, cmdStr string) {
	c.mu.Lock()
	if c.cmdRunning[agent] || time.Since(c.cmdAt[agent]) < 60*time.Second {
		c.mu.Unlock()
		return
	}
	c.cmdRunning[agent] = true
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.cmdRunning[agent] = false
			c.cmdAt[agent] = time.Now()
			c.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sh", "-c", cmdStr).Output()
		if err != nil {
			return
		}
		txt := strings.TrimSpace(string(out))
		if txt == "" {
			return
		}
		if q, ok := ParseUsageScreen(agent, txt); ok {
			c.mu.Lock()
			c.captured[agent] = q
			c.mu.Unlock()
			c.saveCache()
			c.Refresh()
			return
		}
		// Fallback: treat first line as custom summary.
		firstLine := strings.Split(txt, "\n")[0]
		c.mu.Lock()
		c.captured[agent] = protocol.AgentQuota{
			Agent:     agent,
			State:     "ok",
			Summary:   strings.TrimSpace(firstLine),
			Detail:    txt,
			UpdatedAt: time.Now(),
		}
		c.mu.Unlock()
		c.saveCache()
		c.Refresh()
	}()
}

func (c *Collector) loadCache() {
	if c.cachePath == "" {
		return
	}
	b, err := os.ReadFile(c.cachePath)
	if err != nil {
		return
	}
	var m map[string]protocol.AgentQuota
	if json.Unmarshal(b, &m) == nil && m != nil {
		c.captured = m
	}
}

func (c *Collector) saveCache() {
	if c.cachePath == "" {
		return
	}
	c.mu.RLock()
	b, err := json.MarshalIndent(c.captured, "", "  ")
	c.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(c.cachePath), 0o755)
	tmp := c.cachePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, c.cachePath)
	}
}

// --- Codex: reads official /status rate_limits from latest rollout JSONL ---

type codexRolloutLine struct {
	Payload struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary *struct {
				UsedPercent   float64 `json:"used_percent"`
				WindowMinutes int     `json:"window_minutes"`
				ResetsAt      int64   `json:"resets_at"`
			} `json:"primary"`
			Secondary *struct {
				UsedPercent   float64 `json:"used_percent"`
				WindowMinutes int     `json:"window_minutes"`
				ResetsAt      int64   `json:"resets_at"`
			} `json:"secondary"`
			Credits *struct {
				HasCredits bool   `json:"has_credits"`
				Unlimited  bool   `json:"unlimited"`
				Balance    string `json:"balance"`
			} `json:"credits"`
			PlanType             string  `json:"plan_type"`
			RateLimitReachedType *string `json:"rate_limit_reached_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

func (c *Collector) collectCodex() protocol.AgentQuota {
	q := protocol.AgentQuota{
		Agent:     "codex",
		State:     "unknown",
		Summary:   "未采集 (敲 /status)",
		UpdatedAt: time.Now(),
	}
	if c.userHome == "" {
		return q
	}

	pattern := filepath.Join(c.userHome, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return q
	}

	type fileMtime struct {
		path string
		mod  time.Time
	}
	var ranked []fileMtime
	for _, p := range matches {
		if fi, err := os.Stat(p); err == nil {
			ranked = append(ranked, fileMtime{path: p, mod: fi.ModTime()})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].mod.After(ranked[j].mod) })

	for i := 0; i < len(ranked) && i < 5; i++ {
		if parsed, ok := parseCodexRolloutTail(ranked[i].path); ok {
			return parsed
		}
	}
	return q
}

func parseCodexRolloutTail(path string) (protocol.AgentQuota, bool) {
	f, err := os.Open(path)
	if err != nil {
		return protocol.AgentQuota{}, false
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return protocol.AgentQuota{}, false
	}
	const tailSize = 256 * 1024
	if fi.Size() > tailSize {
		_, _ = f.Seek(fi.Size()-tailSize, io.SeekStart)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return protocol.AgentQuota{}, false
	}

	lines := bytes.Split(raw, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, []byte(`"rate_limits"`)) {
			continue
		}
		var ev codexRolloutLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		rl := ev.Payload.RateLimits
		if rl == nil || rl.Primary == nil {
			continue
		}

		win := rl.Primary
		if rl.Secondary != nil && rl.Secondary.UsedPercent > win.UsedPercent {
			win = rl.Secondary
		}

		used := win.UsedPercent
		rem := clampPct(100.0 - used)

		var resetAt *time.Time
		resetStr := ""
		if win.ResetsAt > 0 {
			t := time.Unix(win.ResetsAt, 0)
			resetAt = &t
			if d := time.Until(t); d > 0 {
				resetStr = " " + formatDurationShort(d)
			}
		}

		state := pctState(rem)
		if rl.RateLimitReachedType != nil && *rl.RateLimitReachedType != "" {
			state = "limited"
		}

		winLabel := formatWindowLabel(win.WindowMinutes)
		summary := fmt.Sprintf("%.0f%% 剩%s", rem, resetStr)

		var details []string
		if winLabel != "" {
			details = append(details, fmt.Sprintf("%s limit: %.0f%% left (%.0f%% used)", winLabel, rem, used))
		}
		if rl.Primary != nil && rl.Secondary != nil {
			details = append(details, fmt.Sprintf("5h/Primary: %.0f%% left | Weekly/Secondary: %.0f%% left",
				clampPct(100-rl.Primary.UsedPercent), clampPct(100-rl.Secondary.UsedPercent)))
		}
		if resetAt != nil {
			details = append(details, "Resets: "+resetAt.Format("2006-01-02 15:04"))
		}
		if rl.Credits != nil && rl.Credits.HasCredits && rl.Credits.Balance != "" {
			bal := rl.Credits.Balance
			if dot := strings.IndexByte(bal, '.'); dot > 0 {
				bal = bal[:dot]
			}
			details = append(details, fmt.Sprintf("Credits: %s | Plan: %s", bal, rl.PlanType))
		} else if rl.PlanType != "" {
			details = append(details, "Plan: "+rl.PlanType)
		}

		return protocol.AgentQuota{
			Agent:        "codex",
			State:        state,
			RemainingPct: &rem,
			UsedPct:      &used,
			Summary:      summary,
			Detail:       strings.Join(details, "\n"),
			ResetAt:      resetAt,
			UpdatedAt:    time.Now(),
		}, true
	}
	return protocol.AgentQuota{}, false
}

// --- Universal /usage & /status Screen Parser ---

var (
	usageContextRe  = regexp.MustCompile(`(?i)(/usage|/status|models & quota|limit remaining|current session|current week|weekly limit|five hour limit|5h limit|5-hour|session limit|quota|credits|billing|plan usage|monthly plan|on-demand usage|cursor\.com/dashboard|requests used|estimated cost|spend|usage limit|extra usage)`)
	pctLeftRe       = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*(left|remaining|avail|剩余)`)
	pctLeftPrefixRe = regexp.MustCompile(`(?i)(remaining|left|剩余)\s*[:：]?\s*(\d+(?:\.\d+)?)\s*%`)
	pctUsedRe       = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*(used|consumed|已用)`)
	pctUsedPrefixRe = regexp.MustCompile(`(?i)(used|已用)\s*[:：]?\s*(\d+(?:\.\d+)?)\s*%`)
	barPctRe        = regexp.MustCompile(`\[[█▓▒░#\-\s]+\]\s*(\d+(?:\.\d+)?)\s*%`)
	refreshInRe     = regexp.MustCompile(`(?i)refreshes?\s+in\s+([0-9]+[dhms](?:\s*[0-9]+[dhms])*)`)
	hoursMinsRe     = regexp.MustCompile(`^(\d+)h(?:(\d+)m)?$`)
	ratioRe         = regexp.MustCompile(`(?i)(\d+)\s*/\s*(\d+)\s*(requests?|req|credits?|msgs?|messages?|prompts?|fast)`)
	costRe          = regexp.MustCompile(`(?i)(estimated cost|total cost|session cost|spend|spent|cost|balance|credits)\s*[:：]?\s*(\$\s*\d+(?:\.\d+)?)`)
	resetRe         = regexp.MustCompile(`(?i)(resets?\s+(?:in\s+)?[^\n|·]{2,32})`)
	apiBillingRe    = regexp.MustCompile(`(?i)(api usage billing|usage limits? (?:are )?only available|pay[- ]as[- ]you[- ]go)`)
	cursorTierRe    = regexp.MustCompile(`(?i)usage\s*[·•|-]\s*([A-Za-z0-9_+ -]{2,24})`)
	cursorCatPctRe  = regexp.MustCompile(`(?i)^(included|auto|api)\s+(\d+(?:\.\d+)?)\s*%\s*used\b`)
	cursorOnDemRe   = regexp.MustCompile(`(?i)^on-demand\s+(\$\s*\d+(?:\.\d+)?)`)
)

func compactRefreshDuration(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if m := hoursMinsRe.FindStringSubmatch(s); len(m) >= 2 {
		if h, err := strconv.Atoi(m[1]); err == nil && h >= 24 {
			days := h / 24
			remH := h % 24
			if remH > 0 {
				return fmt.Sprintf("%dd%dh", days, remH)
			}
			return fmt.Sprintf("%dd", days)
		}
	}
	return s
}

// parseCursorUsage parses Cursor CLI's `/usage` report table:
//
//	Usage · Super
//	Monthly plan and on-demand usage
//	Category   Current
//	Included   1% used
//	  Auto     1% used
//	  API      0% used
//	On-Demand  $0
func parseCursorUsage(plainScreen string) (protocol.AgentQuota, bool) {
	lower := strings.ToLower(plainScreen)
	if !strings.Contains(lower, "monthly plan") &&
		!strings.Contains(lower, "on-demand usage") &&
		!strings.Contains(lower, "cursor.com/dashboard") {
		return protocol.AgentQuota{}, false
	}

	var (
		tier         string
		includedUsed *float64
		autoUsed     *float64
		apiUsed      *float64
		onDemand     string
		limitNote    string
	)

	for _, raw := range strings.Split(plainScreen, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if tier == "" {
			if m := cursorTierRe.FindStringSubmatch(line); len(m) >= 2 {
				tier = strings.TrimSpace(m[1])
			}
		}
		if m := cursorCatPctRe.FindStringSubmatch(line); len(m) >= 3 {
			if v, err := strconv.ParseFloat(m[2], 64); err == nil && v >= 0 && v <= 100 {
				cp := clampPct(v)
				switch strings.ToLower(m[1]) {
				case "included":
					includedUsed = &cp
				case "auto":
					autoUsed = &cp
				case "api":
					apiUsed = &cp
				}
			}
			continue
		}
		if m := cursorOnDemRe.FindStringSubmatch(line); len(m) >= 2 {
			onDemand = strings.ReplaceAll(m[1], " ", "")
			continue
		}
		if strings.Contains(strings.ToLower(line), "monthly limit") {
			limitNote = line
		}
	}

	// Choose Included as primary quota metric; fall back to highest used category if Included wasn't matched.
	var primaryUsed *float64
	if includedUsed != nil {
		primaryUsed = includedUsed
	} else if autoUsed != nil {
		primaryUsed = autoUsed
	} else if apiUsed != nil {
		primaryUsed = apiUsed
	}
	if primaryUsed == nil {
		return protocol.AgentQuota{}, false
	}

	used := *primaryUsed
	rem := clampPct(100.0 - used)
	summary := fmt.Sprintf("%.0f%% 剩", rem)
	if tier != "" {
		summary += " · " + tier
	}

	var details []string
	if tier != "" {
		details = append(details, "Plan: "+tier)
	}
	if includedUsed != nil {
		details = append(details, fmt.Sprintf("Included: %.0f%% used (%.0f%% left)", *includedUsed, clampPct(100-*includedUsed)))
	}
	var sub []string
	if autoUsed != nil {
		sub = append(sub, fmt.Sprintf("Auto: %.0f%% used", *autoUsed))
	}
	if apiUsed != nil {
		sub = append(sub, fmt.Sprintf("API: %.0f%% used", *apiUsed))
	}
	if len(sub) > 0 {
		details = append(details, strings.Join(sub, " | "))
	}
	if onDemand != "" {
		details = append(details, "On-Demand: "+onDemand)
	}
	if limitNote != "" {
		details = append(details, limitNote)
	}

	return protocol.AgentQuota{
		Agent:        "cursor",
		State:        pctState(rem),
		RemainingPct: &rem,
		UsedPct:      &used,
		Summary:      summary,
		Detail:       strings.Join(details, "\n"),
		UpdatedAt:    time.Now(),
	}, true
}

// parseAgyModelsQuota parses Antigravity CLI's multi-line "Models & Quota" /usage panel.
func parseAgyModelsQuota(plainScreen string) (protocol.AgentQuota, bool) {
	if !strings.Contains(plainScreen, "Models & Quota") && !strings.Contains(plainScreen, "Limit Remaining") {
		return protocol.AgentQuota{}, false
	}

	type windowEntry struct {
		group   string
		window  string
		remPct  float64
		refresh string
	}

	var entries []windowEntry
	curGroup := "GEMINI"
	curWindow := ""

	lines := strings.Split(plainScreen, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "GEMINI MODELS"):
			curGroup = "Gemini"
			curWindow = ""
			continue
		case strings.HasPrefix(upper, "CLAUDE AND GPT"):
			curGroup = "Claude/GPT"
			curWindow = ""
			continue
		case strings.HasPrefix(upper, "GEMINI 4 ARGON"):
			curGroup = "Argon"
			curWindow = ""
			continue
		}

		lower := strings.ToLower(line)
		if strings.Contains(lower, "weekly limit remaining") {
			curWindow = "7d"
			continue
		}
		if strings.Contains(lower, "five hour limit remaining") || strings.Contains(lower, "5 hour limit remaining") {
			curWindow = "5h"
			continue
		}

		if curWindow != "" {
			if m := barPctRe.FindStringSubmatch(line); len(m) >= 2 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					entries = append(entries, windowEntry{
						group:  curGroup,
						window: curWindow,
						remPct: clampPct(v),
					})
				}
				continue
			}
			if m := refreshInRe.FindStringSubmatch(line); len(m) >= 2 && len(entries) > 0 {
				if entries[len(entries)-1].refresh == "" {
					entries[len(entries)-1].refresh = compactRefreshDuration(m[1])
				}
				continue
			}
		}
	}

	if len(entries) == 0 {
		return protocol.AgentQuota{}, false
	}

	// Antigravity maintains independent quota pools per model family (e.g. Gemini vs Claude/GPT).
	// Compute each pool's effective remaining % (minimum across its 7d/5h windows) so exhausting
	// one pool (e.g. Claude/GPT 5h = 0%) never makes the whole agy agent look 0% limited while
	// another pool (e.g. Gemini = 44%) still has plenty of quota.
	type poolSummary struct {
		group   string
		tag     string
		remPct  float64
		refresh string
	}
	poolTag := func(group string) string {
		switch group {
		case "Gemini":
			return "G"
		case "Claude/GPT":
			return "C"
		case "Argon":
			return "A"
		default:
			if len(group) > 0 {
				return strings.ToUpper(group[:1])
			}
			return "?"
		}
	}

	var pools []poolSummary
	poolIdx := make(map[string]int)
	for _, e := range entries {
		if idx, ok := poolIdx[e.group]; ok {
			if e.remPct < pools[idx].remPct {
				pools[idx].remPct = e.remPct
				pools[idx].refresh = e.refresh
			}
		} else {
			poolIdx[e.group] = len(pools)
			pools = append(pools, poolSummary{
				group:   e.group,
				tag:     poolTag(e.group),
				remPct:  e.remPct,
				refresh: e.refresh,
			})
		}
	}

	var details []string
	for _, e := range entries {
		d := fmt.Sprintf("%s %s: %.0f%% 剩", e.group, e.window, e.remPct)
		if e.refresh != "" {
			d += " (刷新 " + e.refresh + ")"
		}
		details = append(details, d)
	}

	var rem float64
	var state string
	var summary string

	if len(pools) == 1 {
		p := pools[0]
		rem = p.remPct
		state = pctState(rem)
		summary = fmt.Sprintf("%.0f%% 剩", rem)
		if p.refresh != "" {
			summary += " " + p.refresh
		}
	} else {
		minPool := pools[0]
		maxPool := pools[0]
		parts := make([]string, 0, len(pools))
		for _, p := range pools {
			if p.remPct < minPool.remPct {
				minPool = p
			}
			if p.remPct > maxPool.remPct {
				maxPool = p
			}
			parts = append(parts, fmt.Sprintf("%s:%.0f%%", p.tag, p.remPct))
		}
		summary = strings.Join(parts, " ")
		switch {
		case maxPool.remPct <= 0:
			rem = 0
			state = "limited"
		case minPool.remPct <= 0:
			// Partial exhaustion: one pool is 0% while another still has quota.
			rem = maxPool.remPct
			state = "low"
		default:
			rem = minPool.remPct
			state = pctState(rem)
		}
	}

	used := clampPct(100.0 - rem)
	return protocol.AgentQuota{
		Agent:        "agy",
		State:        state,
		RemainingPct: &rem,
		UsedPct:      &used,
		Summary:      summary,
		Detail:       strings.Join(details, "\n"),
		UpdatedAt:    time.Now(),
	}, true
}

// ParseUsageScreen extracts real quota / usage / cost lines from rendered /usage or /status terminal output.
func ParseUsageScreen(agent, plainScreen string) (protocol.AgentQuota, bool) {
	if agent == "agy" {
		if q, ok := parseAgyModelsQuota(plainScreen); ok {
			return q, true
		}
	}
	if agent == "cursor" {
		if q, ok := parseCursorUsage(plainScreen); ok {
			return q, true
		}
	}
	if !usageContextRe.MatchString(plainScreen) {
		return protocol.AgentQuota{}, false
	}

	var matchedLines []string
	var minRem *float64
	var costStr string
	var resetStr string
	isAPIBilling := false

	for _, rawLine := range strings.Split(plainScreen, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		// Skip pure prompt echo lines.
		if line == "/usage" || line == "/status" || line == "> /usage" || line == "› /status" {
			continue
		}

		lineRelevant := false

		if m := pctLeftRe.FindStringSubmatch(line); len(m) >= 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 100 {
				v = clampPct(v)
				if minRem == nil || v < *minRem {
					cp := v
					minRem = &cp
				}
				lineRelevant = true
			}
		} else if m := pctLeftPrefixRe.FindStringSubmatch(line); len(m) >= 3 {
			if v, err := strconv.ParseFloat(m[2], 64); err == nil && v >= 0 && v <= 100 {
				v = clampPct(v)
				if minRem == nil || v < *minRem {
					cp := v
					minRem = &cp
				}
				lineRelevant = true
			}
		} else if m := pctUsedRe.FindStringSubmatch(line); len(m) >= 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= 0 && v <= 100 {
				rem := clampPct(100.0 - v)
				if minRem == nil || rem < *minRem {
					cp := rem
					minRem = &cp
				}
				lineRelevant = true
			}
		} else if m := pctUsedPrefixRe.FindStringSubmatch(line); len(m) >= 3 {
			if v, err := strconv.ParseFloat(m[2], 64); err == nil && v >= 0 && v <= 100 {
				rem := clampPct(100.0 - v)
				if minRem == nil || rem < *minRem {
					cp := rem
					minRem = &cp
				}
				lineRelevant = true
			}
		}

		if m := ratioRe.FindStringSubmatch(line); len(m) >= 3 {
			usedN, err1 := strconv.ParseFloat(m[1], 64)
			totalN, err2 := strconv.ParseFloat(m[2], 64)
			if err1 == nil && err2 == nil && totalN > 0 && usedN <= totalN {
				rem := clampPct((1.0 - usedN/totalN) * 100.0)
				if minRem == nil || rem < *minRem {
					cp := rem
					minRem = &cp
				}
				lineRelevant = true
			}
		}

		if m := costRe.FindStringSubmatch(line); len(m) >= 3 {
			if costStr == "" {
				costStr = strings.ReplaceAll(m[2], " ", "")
			}
			lineRelevant = true
		}

		if m := resetRe.FindStringSubmatch(line); len(m) >= 2 {
			if resetStr == "" {
				resetStr = strings.TrimSpace(m[1])
			}
			lineRelevant = true
		}

		if apiBillingRe.MatchString(line) {
			isAPIBilling = true
			lineRelevant = true
		}

		if lineRelevant && len(matchedLines) < 8 {
			matchedLines = append(matchedLines, line)
		}
	}

	if minRem == nil && costStr == "" && !isAPIBilling {
		return protocol.AgentQuota{}, false
	}

	q := protocol.AgentQuota{
		Agent:     agent,
		State:     "ok",
		Detail:    strings.Join(matchedLines, "\n"),
		UpdatedAt: time.Now(),
	}

	if minRem != nil {
		rem := *minRem
		used := clampPct(100.0 - rem)
		q.RemainingPct = &rem
		q.UsedPct = &used
		q.State = pctState(rem)
		if costStr != "" {
			q.Summary = fmt.Sprintf("%.0f%% 剩 · %s", rem, costStr)
		} else {
			q.Summary = fmt.Sprintf("%.0f%% 剩", rem)
		}
		return q, true
	}

	if costStr != "" {
		if strings.Contains(strings.ToLower(plainScreen), "estimated cost") {
			q.Summary = "API 按量 · " + costStr
		} else {
			q.Summary = "费用 " + costStr
		}
		return q, true
	}

	if isAPIBilling {
		q.Summary = "API 按量计费"
		return q, true
	}

	return protocol.AgentQuota{}, false
}

// --- Live PTY Rate-Limit Pattern Detector ---

var (
	// Strict error-line patterns so code/diffs/comments mentioning "rate limit" never cause false positives.
	rateLimitStrongRe = regexp.MustCompile(`(?i)(\b(?:http|status|error)\s*:?\s*429\b|\b429\s+too many requests\b|\bresource_exhausted\b|\binsufficient_quota\b|you['’]?ve hit your [a-z0-9 -]{0,24}limit|you have reached your [a-z0-9 -]{0,24}limit|credit balance is too low|\bout of credits\b)`)
	rateLimitLineRe   = regexp.MustCompile(`(?i)^(?:error|api error|warning|fatal|failed|✗|×|⚠|!)\s*[:\-]?\s*.*(?:rate[ -]?limit|too many requests|quota exceeded|usage limit (?:reached|exceeded))|^(?:rate[ -]?limit(?:ed| exceeded| reached)|api usage limit (?:reached|exceeded)|usage limit (?:reached|exceeded)|quota exceeded)\b`)
	codeLinePrefixRe  = regexp.MustCompile(`^(?://|/\*|\*|#|\+|-|\||│|├|└|▎|\d+\s*[:|])`)
	ansiStripRe       = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

func looksLikeCodeOrDiffLine(line string) bool {
	if len(line) > 220 {
		return true
	}
	if codeLinePrefixRe.MatchString(line) {
		return true
	}
	// Source code constructs, string literals, JSON, or regexes.
	for _, tok := range []string{":=", "==", "!=", "=>", "->", "func ", "var ", "const ", "regexp.", "MustCompile", "MarkRateLimited", "DetectRateLimit", "```", "`", `"`, ";", "{", "}"} {
		if strings.Contains(line, tok) {
			return true
		}
	}
	return false
}

func DetectRateLimit(raw []byte) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	plain := ansiStripRe.ReplaceAllString(string(raw), "")
	for _, rawLine := range strings.Split(plain, "\n") {
		line := strings.TrimSpace(strings.ReplaceAll(rawLine, "\r", ""))
		if line == "" || looksLikeCodeOrDiffLine(line) {
			continue
		}
		if loc := rateLimitStrongRe.FindString(line); loc != "" {
			return strings.TrimSpace(loc), true
		}
		if loc := rateLimitLineRe.FindString(line); loc != "" {
			return strings.TrimSpace(loc), true
		}
	}
	return "", false
}

// collectCursorLocked returns cached Cursor subscription tier (fetching asynchronously).
// Must be called with c.mu held.
func (c *Collector) collectCursorLocked() protocol.AgentQuota {
	q := protocol.AgentQuota{
		Agent:     "cursor",
		State:     "ok",
		Summary:   "正常 (IDE看配额)",
		UpdatedAt: time.Now(),
	}
	tier := c.cursorBio
	needFetch := !c.cursorFetching && (tier == "" || time.Since(c.cursorAt) > 10*time.Minute)
	if needFetch {
		c.cursorFetching = true
		go func() {
			defer func() {
				c.mu.Lock()
				c.cursorFetching = false
				c.mu.Unlock()
			}()
			bin, err := exec.LookPath("cursor-agent")
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "about", "--format", "json").Output()
			if err != nil {
				return
			}
			var info struct {
				SubscriptionTier string `json:"subscriptionTier"`
			}
			if json.Unmarshal(out, &info) == nil && info.SubscriptionTier != "" {
				c.mu.Lock()
				c.cursorBio = info.SubscriptionTier
				c.cursorAt = time.Now()
				c.mu.Unlock()
				c.Refresh()
			}
		}()
	}
	if tier != "" {
		q.Summary = tier + " · 正常"
	}
	return q
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return math.Round(v*10) / 10
}

func pctState(rem float64) string {
	switch {
	case rem <= 0:
		return "limited"
	case rem <= 20:
		return "low"
	default:
		return "ok"
	}
}

func formatWindowLabel(mins int) string {
	switch {
	case mins <= 0:
		return ""
	case mins >= 1440:
		return fmt.Sprintf("%dd", mins/1440)
	case mins >= 60:
		return fmt.Sprintf("%dh", mins/60)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}
