package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/loucongV/all-in-one/internal/protocol"
)

type CodexAdapter struct{}

func (a *CodexAdapter) Name() string   { return "codex" }
func (a *CodexAdapter) Label() string  { return "Codex CLI" }
func (a *CodexAdapter) Binary() string { return "codex" }

func (a *CodexAdapter) ResolveBinary() (string, bool) {
	return resolveToolBinary(a.Name(), "codex")
}

func (a *CodexAdapter) BuildLaunch(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}

	// Run Codex in inline (--no-alt-screen) mode so all output naturally flows into aio's
	// 10,000-line VT scrollback buffer (matching Claude Code, Antigravity, Cursor, and Shell)
	// with smooth universal wheel/PgUp scrolling and zero alt-screen overlay glitches.
	args := []string{"--no-alt-screen"}
	if spec.WorkDir != "" {
		args = append(args, "-C", spec.WorkDir)
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}

	switch spec.PermMode {
	case protocol.PermAuto:
		args = append(args, "--approve-for-me")
	case protocol.PermPlan:
		args = append(args, "-s", "read-only")
	case protocol.PermYolo:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}

	if spec.SelfBin != "" && spec.RunID != "" {
		notifyArr, _ := json.Marshal([]string{spec.SelfBin, "hook", "--run", spec.RunID, "--event", "done"})
		args = append(args, "-c", fmt.Sprintf("notify=%s", string(notifyArr)))
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

func (a *CodexAdapter) BuildResume(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}
	// Run Codex in inline (--no-alt-screen) mode so all output naturally flows into aio's
	// 10,000-line VT scrollback buffer (matching Claude Code, Antigravity, Cursor, and Shell)
	// with smooth universal wheel/PgUp scrolling and zero alt-screen overlay glitches.
	args := []string{"--no-alt-screen"}
	if spec.WorkDir != "" {
		args = append(args, "-C", spec.WorkDir)
	}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	switch spec.PermMode {
	case protocol.PermAuto:
		args = append(args, "--approve-for-me")
	case protocol.PermPlan:
		args = append(args, "-s", "read-only")
	case protocol.PermYolo:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if spec.SelfBin != "" && spec.RunID != "" {
		notifyArr, _ := json.Marshal([]string{spec.SelfBin, "hook", "--run", spec.RunID, "--event", "done"})
		args = append(args, "-c", fmt.Sprintf("notify=%s", string(notifyArr)))
	}

	switch spec.ResumeMode {
	case "picker":
		args = append(args, "resume")
	case "id":
		if spec.SessionID != "" {
			args = append(args, "resume", spec.SessionID)
		} else {
			args = append(args, "resume")
		}
	case "fresh":
		// Start a clean new interactive Codex session in the same workspace.
	default: // "last" or empty
		args = append(args, "resume", "--last")
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}
