package cliprobe

import (
	"context"
	"os/exec"
	"path/filepath"

	"github.com/holark-ai/holark/internal/commandprefix"
)

// Observation separates executable resolution from the optional version command.
// Path is absolute when resolution succeeds, even if VersionError is non-nil.
type Observation struct {
	Path            string
	Output          string
	ResolutionError error
	VersionError    error
}

// Discovery is the boundary between installed executables and version classification.
// Execution always uses real commands; supplying discovery metadata cannot emulate a CLI.
type Discovery interface {
	Discover(context.Context, string) Observation
}

type Installed struct{}

func (Installed) Discover(ctx context.Context, executable string) Observation {
	return (Installed{}).DiscoverPrefix(ctx, commandprefix.New(executable))
}

func (Installed) DiscoverPrefix(ctx context.Context, prefix commandprefix.Prefix) Observation {
	path, err := exec.LookPath(prefix.Executable())
	if err != nil {
		return Observation{ResolutionError: err}
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Observation{ResolutionError: err}
	}
	output, err := VersionPrefix(ctx, commandprefix.New(path, prefix.Arguments()...))
	return Observation{Path: path, Output: output, VersionError: err}
}
