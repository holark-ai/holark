// Package gittransport keeps provider repository identities separate from the
// Git URLs configured by the user for authentication and transport.
package gittransport

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

// Resolve selects a configured URL for source without changing Git config.
// Push URLs are considered separately so a read-only fetch endpoint is never
// accidentally used instead of a configured publication endpoint.
func Resolve(ctx context.Context, directory, source string, push bool) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" || source == "origin" {
		return "origin", nil
	}
	run := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = directory
		output, err := command.Output()
		return strings.TrimSpace(string(output)), err
	}
	remotes, err := run("remote")
	if err != nil {
		return "", fmt.Errorf("list Git remotes: %w", err)
	}
	names := strings.Fields(remotes)
	// Prefer origin when several remotes use different credentials for one host.
	for i, name := range names {
		if name == "origin" {
			names[0], names[i] = names[i], names[0]
			break
		}
	}
	var configured []string
	for _, name := range names {
		key := "remote." + name + ".url"
		if push {
			key = "remote." + name + ".pushurl"
		}
		value, err := run("config", "--get-all", key)
		if err != nil && !missingConfig(err) {
			return "", fmt.Errorf("read Git remote URL: %w", err)
		}
		if push && value == "" {
			value, err = run("config", "--get-all", "remote."+name+".url")
			if err != nil && !missingConfig(err) {
				return "", fmt.Errorf("read Git remote URL: %w", err)
			}
		}
		if value != "" {
			configured = append(configured, strings.Split(value, "\n")...)
		}
	}
	return SelectURL(source, configured...), nil
}

func missingConfig(err error) bool {
	value, ok := err.(*exec.ExitError)
	return ok && value.ExitCode() == 1
}

// SelectURL first reuses an exact repository's configured transport. For a
// fork without its own remote, it carries the same host's SSH transport across
// while retaining the requested fork path. Other hosts are left untouched.
func SelectURL(source string, configured ...string) string {
	target, ok := parse(source)
	if !ok {
		return source
	}
	for _, candidate := range configured {
		remote, ok := parse(candidate)
		if ok && remote.host == target.host && samePath(remote.host, remote.path, target.path) {
			return candidate
		}
	}
	for _, candidate := range configured {
		remote, ok := parse(candidate)
		if ok && remote.ssh && remote.host == target.host {
			return remote.withPath(target.path)
		}
	}
	return source
}

type remoteURL struct {
	host, path, prefix string
	ssh                bool
	parsed             *url.URL
}

func parse(raw string) (remoteURL, bool) {
	if !strings.Contains(raw, "://") {
		prefix, path, ok := strings.Cut(raw, ":")
		if !ok || strings.ContainsAny(prefix, "/\\") || !strings.Contains(prefix, "@") || path == "" {
			return remoteURL{}, false
		}
		_, host, _ := strings.Cut(prefix, "@")
		return remoteURL{host: strings.ToLower(host), path: path, prefix: prefix + ":", ssh: true}, true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.Path == "" || (parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "ssh") {
		return remoteURL{}, false
	}
	return remoteURL{host: strings.ToLower(parsed.Hostname()), path: strings.TrimPrefix(parsed.Path, "/"), ssh: parsed.Scheme == "ssh", parsed: parsed}, true
}

func samePath(host, left, right string) bool {
	left = strings.TrimSuffix(strings.TrimRight(left, "/"), ".git")
	right = strings.TrimSuffix(strings.TrimRight(right, "/"), ".git")
	if host == "github.com" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (remote remoteURL) withPath(path string) string {
	if remote.parsed == nil {
		return remote.prefix + path
	}
	value := *remote.parsed
	value.Path, value.RawPath = "/"+path, ""
	return value.String()
}
