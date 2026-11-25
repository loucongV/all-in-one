package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/loucongV/all-in-one/internal/protocol"
)

type AgyAdapter struct{}

func (a *AgyAdapter) Name() string   { return "agy" }
func (a *AgyAdapter) Label() string  { return "Antigravity" }
func (a *AgyAdapter) Binary() string { return "agy" }

func (a *AgyAdapter) ResolveBinary() (string, bool) {
	return resolveToolBinary(a.Name(), "agy")
}

func (a *AgyAdapter) BuildLaunch(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}

	var args []string
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}

	switch spec.PermMode {
	case protocol.PermAuto:
		args = append(args, "--mode", "accept-edits")
	case protocol.PermPlan:
		args = append(args, "--mode", "plan")
	case protocol.PermYolo:
		args = append(args, "--dangerously-skip-permissions")
	}

	if settingsJSON := buildAgyHookSettings(spec); settingsJSON != "" {
		args = append(args, "--settings", settingsJSON)
	}

	if spec.Prompt != "" {
		args = append(args, "-i", spec.Prompt)
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}

func (a *AgyAdapter) BuildResume(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}
	var args []string
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	switch spec.PermMode {
	case protocol.PermAuto:
		args = append(args, "--mode", "accept-edits")
	case protocol.PermPlan:
		args = append(args, "--mode", "plan")
	case protocol.PermYolo:
		args = append(args, "--dangerously-skip-permissions")
	}

	if settingsJSON := buildAgyHookSettings(spec); settingsJSON != "" {
		args = append(args, "--settings", settingsJSON)
	}

	switch spec.ResumeMode {
	case "id":
		if spec.SessionID != "" {
			args = append(args, "--conversation", spec.SessionID)
		} else {
			args = append(args, "-c")
		}
	case "fresh":
		// Fresh interactive agy session.
	default: // "last" or "picker"
		args = append(args, "-c")
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}

// buildAgyHookSettings injects a session-only --settings JSON block using agy's
// official `title` command contract (never persisted to ~/.gemini/antigravity-cli/settings.json).
// Whenever agy's agent_state or tool_confirmation_pending changes, agy pipes the live
// JSON state payload to `aio hook --run <id> --agy-state`.
func buildAgyHookSettings(spec LaunchSpec) string {
	if spec.SelfBin == "" || spec.RunID == "" {
		return ""
	}
	cmd := fmt.Sprintf("%q hook --run %q --agy-state", spec.SelfBin, spec.RunID)
	payload := map[string]any{
		"title": map[string]any{
			"type":    "command",
			"command": cmd,
			"enabled": true,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}
