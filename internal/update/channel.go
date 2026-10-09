package update

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	// ChannelStable tracks the repository's ordinary GitHub "latest" release.
	// Prerelease and CellBridge tags are never installed on this channel.
	ChannelStable = "stable"
	// ChannelCellbridge tracks published Halo+VoCat+CellBridge releases.
	ChannelCellbridge = "cellbridge"
)

// cellbridgeTag matches halo-X.Y.Z-cellbridge, an optional v prefix, and
// halo-X.Y.Z-cellbridge-preview.N. N may be a large UTC timestamp. The halo-
// prefix is removed only after this pattern matches.
var cellbridgeTag = regexp.MustCompile(`^(?:halo-)?v?(\d+\.\d+\.\d+)-cellbridge(?:-preview\.([0-9]+))?$`)

type channelContextKey struct{}

// WithChannel forces the update channel for this context. Production callers
// leave it unset and resolve VOCAT_UPDATE_CHANNEL or the running version.
func WithChannel(ctx context.Context, channel string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, channelContextKey{}, strings.TrimSpace(channel))
}

func channelFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(channelContextKey{}).(string)
	return strings.TrimSpace(value)
}

// Channel is the channel the process will check. An explicit VOCAT_UPDATE_CHANNEL
// wins. With no environment variable, a build whose version contains
// "-cellbridge" uses the cellbridge channel; every other build stays on stable.
func Channel() string {
	channel, err := resolveChannel(context.Background(), "")
	if err != nil {
		return ""
	}
	return channel
}

func resolveChannel(ctx context.Context, explicit string) (string, error) {
	return resolveUpdateChannel(explicit, channelFromContext(ctx), os.Getenv("VOCAT_UPDATE_CHANNEL"), runningVersion())
}

func resolveUpdateChannel(explicit, contextValue, envValue, version string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(explicit))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(contextValue))
	}
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(envValue))
	}
	switch value {
	case "":
		if strings.Contains(version, "-cellbridge") {
			return ChannelCellbridge, nil
		}
		return ChannelStable, nil
	case ChannelStable, ChannelCellbridge:
		return value, nil
	default:
		return "", fmt.Errorf("update: unsupported update channel %q", value)
	}
}

// cellbridgeReleaseVersion returns the semantic version for a verified
// CellBridge tag. The halo- prefix is removed only after the tag matches.
func cellbridgeReleaseVersion(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	match := cellbridgeTag.FindStringSubmatch(tag)
	if match == nil {
		return "", false
	}
	version := match[1] + "-cellbridge"
	if match[2] != "" {
		if len(match[2]) > 1 && match[2][0] == '0' {
			return "", false
		}
		version += "-preview." + match[2]
	}
	if _, err := parseSemanticVersion(version); err != nil {
		return "", false
	}
	return version, true
}

func versionMatchesChannel(version, channel string) error {
	cellbridge := strings.Contains(version, "-cellbridge")
	switch channel {
	case ChannelCellbridge:
		if !cellbridge {
			return fmt.Errorf("update: release %s is not a cellbridge build", version)
		}
	case ChannelStable:
		if cellbridge {
			return fmt.Errorf("update: release %s is a cellbridge build and is not on the stable channel", version)
		}
	default:
		return fmt.Errorf("update: unsupported update channel %q", channel)
	}
	return nil
}
