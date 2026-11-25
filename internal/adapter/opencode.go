package adapter

// OpenCodeAdapter integrates the open-source OpenCode terminal coding agent (`opencode`).
type OpenCodeAdapter struct{}

func (a *OpenCodeAdapter) Name() string   { return "opencode" }
func (a *OpenCodeAdapter) Label() string  { return "OpenCode" }
func (a *OpenCodeAdapter) Binary() string { return "opencode" }

func (a *OpenCodeAdapter) ResolveBinary() (string, bool) {
	return resolveToolBinary(a.Name(), "opencode")
}

func (a *OpenCodeAdapter) BuildLaunch(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}

	var args []string
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Prompt != "" {
		args = append(args, "--prompt", spec.Prompt)
	}
	if spec.WorkDir != "" {
		args = append(args, spec.WorkDir)
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}

func (a *OpenCodeAdapter) BuildResume(spec LaunchSpec) (CommandSpec, error) {
	bin, ok := a.ResolveBinary()
	if !ok {
		bin = a.Binary()
	}

	var args []string
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}

	switch spec.ResumeMode {
	case "picker":
		// OpenCode's TUI has built-in session switching (/sessions) when launched cleanly.
	case "id":
		if spec.SessionID != "" {
			args = append(args, "--session", spec.SessionID)
		} else {
			args = append(args, "--continue")
		}
	case "fresh":
		// Start a clean new interactive OpenCode session.
	default: // "last"
		args = append(args, "--continue")
	}

	if spec.WorkDir != "" {
		args = append(args, spec.WorkDir)
	}

	return CommandSpec{
		Binary: bin,
		Args:   args,
		Env:    baseEnv(spec),
		Dir:    spec.WorkDir,
	}, nil
}
