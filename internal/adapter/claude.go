package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/loucongV/all-in-one/internal/protocol"
)

type ClaudeAdapter struct{}

func (a *ClaudeAdapter) Name() string   { return "claude" }
func (a *ClaudeAdapter) Label() string  { return "Claude Code" }
func (a *ClaudeAdapter) Binary() string { return "claude" }

func (a *ClaudeAdapter) ResolveBinary() (string, bool) {
	return resolveToolBinary(a.Name(), "claude")
}

func (a *ClaudeAdapter) BuildLaunch(spec LaunchSpec) (CommandSpec, error) {
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
		args = append(args, "--permission-mode", "acceptEdits")
	case protocol.PermPlan:
		args = append(args, "--permission-mode", "plan")
	case protocol.PermYolo:
		args = append(args, "--dangerously-skip-permissions")
	}

	if spec.SessionID != "" {
		args = append(args, "--session-id", spec.SessionID)
	}

	if settingsJSON := buildClaudeHookSettings(spec); settingsJSON != "" {
		args = append(args, "--settings", settingsJSON)
	}

	if spec.Prompt != "" {
		args = append(args, spec.Prompt)
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}

func (a *ClaudeAdapter) BuildResume(spec LaunchSpec) (CommandSpec, error) {
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
		args = append(args, "--permission-mode", "acceptEdits")
	case protocol.PermPlan:
		args = append(args, "--permission-mode", "plan")
	case protocol.PermYolo:
		args = append(args, "--dangerously-skip-permissions")
	}

	if settingsJSON := buildClaudeHookSettings(spec); settingsJSON != "" {
		args = append(args, "--settings", settingsJSON)
	}

	switch spec.ResumeMode {
	case "picker":
		args = append(args, "--resume")
	case "id":
		if spec.SessionID != "" {
			args = append(args, "--resume", spec.SessionID)
		} else {
			args = append(args, "--resume")
		}
	case "fresh":
		// Fresh interactive Claude session with optional deterministic UUID binding.
		if spec.SessionID != "" {
			args = append(args, "--session-id", spec.SessionID)
		}
	default: // "last"
		args = append(args, "--continue")
	}
	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}

func buildClaudeHookSettings(spec LaunchSpec) string {
	if spec.SelfBin == "" || spec.RunID == "" {
		return ""
	}
	hookCmd := func(event string) string {
		return fmt.Sprintf("%q hook --run %q --event %s", spec.SelfBin, spec.RunID, event)
	}
	payload := map[string]any{
		"hooks": map[string]any{
			"Notification": []map[string]any{
				{
					"hooks": []map[string]any{
						{"type": "command", "command": hookCmd("waiting")},
					},
				},
			},
			"Stop": []map[string]any{
				{
					"hooks": []map[string]any{
						{"type": "command", "command": hookCmd("done")},
					},
				},
			},
			"PreToolUse": []map[string]any{
				{
					"hooks": []map[string]any{
						{"type": "command", "command": hookCmd("running")},
					},
				},
			},
			"UserPromptSubmit": []map[string]any{
				{
					"hooks": []map[string]any{
						{"type": "command", "command": hookCmd("running")},
					},
				},
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}
