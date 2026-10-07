// Package commandprefix represents a literal executable and its leading arguments.
// Commands are never evaluated by a shell.
package commandprefix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/holark-ai/holark/internal/protocol"
)

type Prefix struct {
	executable string
	arguments  []string
}

func New(executable string, arguments ...string) Prefix {
	return Prefix{executable, append([]string(nil), arguments...)}
}
func (p Prefix) Executable() string  { return p.executable }
func (p Prefix) Arguments() []string { return append([]string(nil), p.arguments...) }
func (p Prefix) Key() string {
	data, _ := json.Marshal(append([]string{p.executable}, p.arguments...))
	return string(data)
}
func (p Prefix) Command(args ...string) *exec.Cmd {
	return exec.Command(p.executable, append(p.Arguments(), args...)...)
}

// CommandContext owns a process group so cancellation also stops wrapper children.
// Callers ending a command early must call Cancel before Wait to clean up the group.
func (p Prefix) CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.executable, append(p.Arguments(), args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return cmd
}

type Resolver interface {
	ResolveCommand(context.Context, protocol.HarnessType) (Prefix, error)
}

func Resolve(ctx context.Context, resolver Resolver, kind protocol.HarnessType, fallback string) (Prefix, error) {
	if resolver != nil {
		return resolver.ResolveCommand(ctx, kind)
	}
	return New(fallback), nil
}

// Parse accepts shell-style word quoting and escaping, without expansion.
// Shell operators must be quoted or escaped to be passed as literal arguments.
// Executables must be absolute paths or bare command names resolved through PATH.
func Parse(input string) (Prefix, error) {
	if !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return Prefix{}, errors.New("command contains invalid text")
	}
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if c == '\\' && quote != '\'' {
			if i+1 == len(runes) {
				return Prefix{}, errors.New("command ends with an escape")
			}
			next := runes[i+1]
			if quote == '"' && next != '"' && next != '\\' && next != '$' && next != '`' && next != '\n' {
				word.WriteRune(c)
				started = true
				continue
			}
			i++
			if next != '\n' {
				word.WriteRune(next)
				started = true
			}
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if c == '\n' || c == '\r' || strings.ContainsRune("|&;<>()", c) {
			return Prefix{}, errors.New("use a wrapper script for shell operators or multiple commands")
		}
		if unicode.IsSpace(c) {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(c)
		started = true
	}
	if quote != 0 {
		return Prefix{}, errors.New("command has an unclosed quote")
	}
	if started {
		words = append(words, word.String())
	}
	if len(words) == 0 || words[0] == "" {
		return Prefix{}, errors.New("command needs an executable")
	}
	if !filepath.IsAbs(words[0]) && strings.ContainsAny(words[0], `/\`) {
		return Prefix{}, errors.New("relative executable paths are not supported; use an absolute path or a command name on PATH")
	}
	return New(words[0], words[1:]...), nil
}
