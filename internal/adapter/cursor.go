package adapter

import "github.com/loucongV/all-in-one/internal/protocol"

type CursorAdapter struct{}

func (a *CursorAdapter) Name() string   { return "cursor" }
func (a *CursorAdapter) Label() string  { return "Cursor CLI" }
func (a *CursorAdapter) Binary() string { return "cursor-agent" }

func (a *CursorAdapter) ResolveBinary() (string, bool) {
	return resolveToolBinary(a.Name(), "cursor-agent", "agent")
}

func (a *CursorAdapter) BuildLaunch(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}

	args := []string{"--trust"}
	if spec.WorkDir != "" {
		args = append(args, "--workspace", spec.WorkDir)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}

	switch spec.PermMode {
	case protocol.PermAuto:
		args = append(args, "--auto-review")
	case protocol.PermPlan:
		args = append(args, "--mode", "plan")
	case protocol.PermYolo:
		args = append(args, "--yolo")
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

func (a *CursorAdapter) BuildResume(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}
	args := []string{"--trust"}
	if spec.WorkDir != "" {
		args = append(args, "--workspace", spec.WorkDir)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	switch spec.PermMode {
	case protocol.PermAuto:
		args = append(args, "--auto-review")
	case protocol.PermPlan:
		args = append(args, "--mode", "plan")
	case protocol.PermYolo:
		args = append(args, "--yolo")
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
		// Fresh interactive Cursor CLI session.
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
