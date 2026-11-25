package adapter

import "github.com/loucongV/all-in-one/internal/protocol"

// KimiAdapter integrates Moonshot AI's Kimi Code CLI (`kimi`).
type KimiAdapter struct{}

func (a *KimiAdapter) Name() string   { return "kimi" }
func (a *KimiAdapter) Label() string  { return "Kimi Code" }
func (a *KimiAdapter) Binary() string { return "kimi" }

func (a *KimiAdapter) ResolveBinary() (string, bool) {
	return resolveToolBinary(a.Name(), "kimi", "kimi-code")
}

func (a *KimiAdapter) BuildLaunch(spec LaunchSpec) (CommandSpec, error) {
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
		args = append(args, "--yolo")
	case protocol.PermPlan:
		args = append(args, "--plan")
	case protocol.PermYolo:
		args = append(args, "--auto")
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}

func (a *KimiAdapter) BuildResume(spec LaunchSpec) (CommandSpec, error) {
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
		args = append(args, "--yolo")
	case protocol.PermPlan:
		args = append(args, "--plan")
	case protocol.PermYolo:
		args = append(args, "--auto")
	}

	switch spec.ResumeMode {
	case "picker":
		args = append(args, "--session")
	case "id":
		if spec.SessionID != "" {
			args = append(args, "--session", spec.SessionID)
		} else {
			args = append(args, "--session")
		}
	case "fresh":
		// Fresh interactive Kimi Code session in the same workspace.
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
