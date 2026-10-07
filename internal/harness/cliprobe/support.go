package cliprobe

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/holark-ai/holark/internal/commandprefix"
)

// ParsedVersion retains prerelease identity. Build metadata does not affect support.
type ParsedVersion struct {
	Numbers    [3]uint64
	Prerelease string
}

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func ParseVersion(value string) (ParsedVersion, bool) {
	match := semanticVersion.FindStringSubmatch(value)
	if match == nil {
		return ParsedVersion{}, false
	}
	var version ParsedVersion
	for i := range version.Numbers {
		n, err := strconv.ParseUint(match[i+1], 10, 64)
		if err != nil {
			return ParsedVersion{}, false
		}
		version.Numbers[i] = n
	}
	version.Prerelease = match[4]
	for _, part := range strings.Split(version.Prerelease, ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return ParsedVersion{}, false
		}
	}
	return version, true
}

// Compare orders versions by numeric precedence, then by prerelease precedence:
// a release outranks any prerelease with equal numbers, and prerelease
// identifiers compare per semver (numeric identifiers order numerically and
// rank below alphanumeric ones; a shorter prefix ranks below its extension).
func (v ParsedVersion) Compare(o ParsedVersion) int {
	for i := range v.Numbers {
		if v.Numbers[i] < o.Numbers[i] {
			return -1
		}
		if v.Numbers[i] > o.Numbers[i] {
			return 1
		}
	}
	switch {
	case v.Prerelease == o.Prerelease:
		return 0
	case v.Prerelease == "":
		return 1
	case o.Prerelease == "":
		return -1
	}
	left, right := strings.Split(v.Prerelease, "."), strings.Split(o.Prerelease, ".")
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		leftNumber, leftErr := strconv.ParseUint(left[i], 10, 64)
		rightNumber, rightErr := strconv.ParseUint(right[i], 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			return 1
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case left[i] < right[i]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	default:
		return 0
	}
}

// AtLeastVersion reports whether value is strict semver at or above
// maj.min.pat. Unparseable input returns false so callers fall back to the
// validated behavior. The check is pure and performs no I/O.
func AtLeastVersion(value string, maj, min, pat uint64) bool {
	parsed, ok := ParseVersion(value)
	if !ok {
		return false
	}
	return parsed.Compare(ParsedVersion{Numbers: [3]uint64{maj, min, pat}}) >= 0
}

// SupportPolicy names releases validated by this Holark release. Exact ranges
// deliberately make no promise about unvalidated future patches or prereleases.
type SupportPolicy struct {
	Versions []string
	Latest   string
}

type Options struct {
	Prefix        *commandprefix.Prefix
	Discovery     Discovery
	SupportPolicy *SupportPolicy
}

func Probe(ctx context.Context, executable, label, missingReason string, policy SupportPolicy, normalize func(string) string, options Options) Result {
	discovery := options.Discovery
	if discovery == nil {
		discovery = Installed{}
	}
	if options.SupportPolicy != nil {
		policy = *options.SupportPolicy
	}
	var observation Observation
	if options.Prefix != nil {
		if prefixed, ok := discovery.(interface {
			DiscoverPrefix(context.Context, commandprefix.Prefix) Observation
		}); ok {
			observation = prefixed.DiscoverPrefix(ctx, *options.Prefix)
		} else if len(options.Prefix.Arguments()) == 0 {
			observation = discovery.Discover(ctx, options.Prefix.Executable())
		} else {
			observation = (Installed{}).DiscoverPrefix(ctx, *options.Prefix)
		}
	} else {
		observation = discovery.Discover(ctx, executable)
	}
	return Classify(observation, label, missingReason, policy, normalize)
}

type Result struct {
	Available              bool
	Path                   string
	RawVersion             string
	Version                string
	ParsedVersion          *ParsedVersion
	Reason                 string
	SupportStatus          string
	SupportedRanges        []string
	LatestSupportedVersion string
	Warning                string
}

// Classify is shared by installed discovery and development discovery inputs.
// normalize recognizes a harness's output wrapper without changing version identity.
func Classify(observation Observation, label, missingReason string, policy SupportPolicy, normalize func(string) string) Result {
	result := Result{Path: observation.Path, RawVersion: observation.Output, LatestSupportedVersion: policy.Latest}
	for _, version := range policy.Versions {
		result.SupportedRanges = append(result.SupportedRanges, "="+version)
	}
	if observation.ResolutionError != nil || observation.Path == "" {
		result.Reason = missingReason
		return result
	}
	result.Available = true
	result.SupportStatus = "unknown"
	output := strings.TrimSpace(observation.Output)
	// Keep raw output internally, but bound unrecognized output for UI display.
	result.Version = output
	if len([]rune(output)) > 128 {
		result.Version = string([]rune(output)[:128]) + "…"
	}
	if observation.VersionError == nil && len(output) <= 128 {
		value := normalize(output)
		if parsed, ok := ParseVersion(value); ok {
			result.Version = value
			result.ParsedVersion = &parsed
			result.SupportStatus = "unsupported"
			for _, supported := range policy.Versions {
				target, valid := ParseVersion(supported)
				if valid && parsed == target {
					result.SupportStatus = "supported"
					break
				}
			}
		}
	}
	if result.SupportStatus == "supported" {
		return result
	}
	supported := fmt.Sprintf("Holark supports %s <= %s.", label, policy.Latest)
	if result.SupportStatus == "unknown" {
		if observation.VersionError != nil {
			result.Warning = supported + " Version check failed; this installation may not work as expected."
		} else {
			result.Warning = supported + " Its version could not be identified and may not work as expected."
		}
	} else {
		result.Warning = fmt.Sprintf("%s %s may not work as expected.", supported, result.Version)
	}
	return result
}
