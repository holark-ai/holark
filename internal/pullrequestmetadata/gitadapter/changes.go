package gitadapter

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"

	"github.com/holark-ai/holark/internal/pullrequestmetadata"
)

type Changes struct{ Path string }

func (changes Changes) SingleCommit(ctx context.Context, base, head string) (*pullrequestmetadata.GeneratedOutput, error) {
	// Resolve revisions first so branch names cannot be interpreted as options.
	resolve := func(ref string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", "-C", changes.Path, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
		return strings.TrimSpace(string(out)), err
	}
	base, err := resolve(base)
	if err != nil {
		return nil, err
	}
	head, err = resolve(head)
	if err != nil {
		return nil, err
	}
	commits, err := exec.CommandContext(ctx, "git", "-C", changes.Path, "rev-list", "--max-count=2", base+".."+head, "--").Output()
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(commits))
	if len(ids) != 1 {
		return nil, nil
	}
	message, err := exec.CommandContext(ctx, "git", "-C", changes.Path, "show", "--no-patch", "--no-show-signature", "--format=%s%x00%b", ids[0], "--").Output()
	if err != nil {
		return nil, err
	}
	title, body, _ := strings.Cut(string(message), "\x00")
	return &pullrequestmetadata.GeneratedOutput{Title: strings.TrimSpace(title), Description: strings.TrimSpace(body)}, nil
}

func (changes Changes) Capture(ctx context.Context, base, head string) (*pullrequestmetadata.Provenance, error) {
	resolve := func(ref string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", "-C", changes.Path, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
		return strings.TrimSpace(string(out)), err
	}
	base, err := resolve(base)
	if err != nil {
		return nil, err
	}
	head, err = resolve(head)
	if err != nil {
		return nil, err
	}
	patch, err := exec.CommandContext(ctx, "git", "-C", changes.Path, "-c", "diff.algorithm=myers", "-c", "diff.relative=false", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--binary", "--full-index", "--unified=0", "--inter-hunk-context=0", "--submodule=short", "--ignore-submodules=none", "--src-prefix=a/", "--dst-prefix=b/", base, head, "--").Output()
	if err != nil {
		return nil, err
	}
	// Omit blob IDs and hunk positions/context labels. Paths, modes, binary
	// contents and added/deleted lines (including whitespace) remain significant.
	var normalized strings.Builder
	for _, line := range strings.Split(string(patch), "\n") {
		if strings.HasPrefix(line, "index ") {
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			line = "@@"
		}
		normalized.WriteString(line)
		normalized.WriteByte('\n')
	}
	messages, err := exec.CommandContext(ctx, "git", "-C", changes.Path, "log", "--reverse", "--topo-order", "--no-show-signature", "--format=%B%x00", base+".."+head, "--").Output()
	if err != nil {
		return nil, err
	}
	return &pullrequestmetadata.Provenance{DiffBaseCommit: base, HeadCommit: head,
		PatchFingerprint:    fmt.Sprintf("%x", sha256.Sum256([]byte(normalized.String()))),
		MessagesFingerprint: fmt.Sprintf("%x", sha256.Sum256(messages))}, nil
}
