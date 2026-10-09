package update

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Release mirrors the subset of the GitHub releases API response that the
// self-updater consumes.
type Release struct {
	TagName    string  `json:"tag_name"`
	Name       string  `json:"name"`
	Body       string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is a single downloadable artifact attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckResult describes a trusted release check without downloading assets.
type CheckResult struct {
	Available    bool
	Applied      bool
	Current      string
	Latest       string
	Channel      string
	ReleaseNotes string
	Release      *Release
}

const (
	DefaultRepository  = "Nan7Li/VoCat"
	UpstreamRepository = "MengMengCode/VoCat"
	releasePageLimit   = 5
)

const releasePageSize = 100

var githubAPI = "https://api.github.com"

type githubBaseKey struct{}
type githubClientKey struct{}

// WithGitHubForTest directs release and asset requests on ctx at a local server.
func WithGitHubForTest(ctx context.Context, base string, client *http.Client) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, githubBaseKey{}, strings.TrimRight(strings.TrimSpace(base), "/"))
	if client != nil {
		ctx = context.WithValue(ctx, githubClientKey{}, client)
	}
	return ctx
}

func githubBase(ctx context.Context) string {
	if ctx != nil {
		if base, ok := ctx.Value(githubBaseKey{}).(string); ok && base != "" {
			return base
		}
	}
	return githubAPI
}

func githubClient(ctx context.Context) *http.Client {
	if ctx != nil {
		if client, ok := ctx.Value(githubClientKey{}).(*http.Client); ok && client != nil {
			return client
		}
	}
	return githubHTTPClient
}

var githubHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	},
}

func validateRepo(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("update: repository not configured (set --repo or VOCAT_REPO)")
	}
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return "", fmt.Errorf("update: invalid repository %q (expected owner/name)", repo)
	}
	return repo, nil
}

// LatestRelease fetches the newest non-prerelease published release for repo
// (form "owner/name"). A non-empty token is sent as a Bearer header, which is
// required for private repositories and lifts the unauthenticated rate limit.
func LatestRelease(ctx context.Context, repo, token string) (*Release, error) {
	repo, err := validateRepo(repo)
	if err != nil {
		return nil, err
	}
	var release Release
	if err := githubGetJSON(ctx, githubBase(ctx)+"/repos/"+repo+"/releases/latest", token, &release); err != nil {
		return nil, err
	}
	return &release, nil
}

func listReleases(ctx context.Context, repo, token string) ([]Release, error) {
	repo, err := validateRepo(repo)
	if err != nil {
		return nil, err
	}
	releases := make([]Release, 0, releasePageSize)
	for page := 1; page <= releasePageLimit; page++ {
		var batch []Release
		endpoint := githubBase(ctx) + "/repos/" + repo + "/releases?per_page=" + strconv.Itoa(releasePageSize) + "&page=" + strconv.Itoa(page)
		if err := githubGetJSON(ctx, endpoint, token, &batch); err != nil {
			return nil, err
		}
		releases = append(releases, batch...)
		if len(batch) < releasePageSize {
			break
		}
	}
	return releases, nil
}

func githubGetJSON(ctx context.Context, endpoint, token string, destination any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := githubClient(ctx).Do(req)
	if err != nil {
		return fmt.Errorf("update: fetch GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("update: GitHub API rejected the request (likely rate-limited): %s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("update: no published release found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: GitHub API returned %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(destination); err != nil {
		return fmt.Errorf("update: decode release JSON: %w", err)
	}
	return nil
}

// CheckStable fetches the ordinary latest release and ignores CellBridge and
// other prerelease tags. Upstream VoCat checks use this so a fusion channel
// cannot redirect them.
func CheckStable(ctx context.Context, repo, token, current string) (CheckResult, error) {
	return checkChannel(ctx, repo, token, current, ChannelStable)
}

// CheckLatest fetches the newest release on the active channel and compares
// semantic versions so development builds are never offered an older release.
func CheckLatest(ctx context.Context, repo, token, current string) (CheckResult, error) {
	channel, err := resolveChannel(ctx, "")
	if err != nil {
		return CheckResult{}, err
	}
	return checkChannel(ctx, repo, token, current, channel)
}

func checkChannel(ctx context.Context, repo, token, current, channel string) (CheckResult, error) {
	switch channel {
	case ChannelStable:
		return checkStable(ctx, repo, token, current)
	case ChannelCellbridge:
		return checkCellbridge(ctx, repo, token, current)
	default:
		return CheckResult{}, fmt.Errorf("update: unsupported update channel %q", channel)
	}
}

func checkStable(ctx context.Context, repo, token, current string) (CheckResult, error) {
	release, err := LatestRelease(ctx, repo, token)
	if err != nil {
		return CheckResult{}, err
	}
	if release.Draft {
		return CheckResult{}, fmt.Errorf("update: latest release %s is an unpublished draft", release.TagName)
	}
	if release.Prerelease {
		return CheckResult{}, fmt.Errorf("update: stable channel does not install prerelease %s", release.TagName)
	}
	latest := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if err := versionMatchesChannel(latest, ChannelStable); err != nil {
		return CheckResult{}, err
	}
	available, err := IsNewerVersion(current, latest)
	if err != nil {
		return CheckResult{}, fmt.Errorf("update: compare release versions: %w", err)
	}
	return CheckResult{
		Available:    available,
		Current:      current,
		Latest:       latest,
		Channel:      ChannelStable,
		ReleaseNotes: strings.TrimSpace(release.Body),
		Release:      release,
	}, nil
}

func checkCellbridge(ctx context.Context, repo, token, current string) (CheckResult, error) {
	releases, err := listReleases(ctx, repo, token)
	if err != nil {
		return CheckResult{}, err
	}
	release, latest, err := newestCellbridgeRelease(releases)
	if err != nil {
		return CheckResult{}, err
	}
	available, err := IsNewerVersion(current, latest)
	if err != nil {
		return CheckResult{}, fmt.Errorf("update: compare release versions: %w", err)
	}
	return CheckResult{
		Available:    available,
		Current:      current,
		Latest:       latest,
		Channel:      ChannelCellbridge,
		ReleaseNotes: strings.TrimSpace(release.Body),
		Release:      release,
	}, nil
}

func newestCellbridgeRelease(releases []Release) (*Release, string, error) {
	var best *Release
	bestVersion := ""
	for index := range releases {
		release := &releases[index]
		if release.Draft {
			continue
		}
		version, ok := cellbridgeReleaseVersion(release.TagName)
		if !ok {
			continue
		}
		if best == nil {
			chosen := *release
			best = &chosen
			bestVersion = version
			continue
		}
		newer, err := IsNewerVersion(bestVersion, version)
		if err != nil || !newer {
			continue
		}
		chosen := *release
		best = &chosen
		bestVersion = version
	}
	if best == nil {
		return nil, "", fmt.Errorf("update: no published cellbridge release")
	}
	return best, bestVersion, nil
}

// downloadAsset streams a release asset into dst, honoring the request context.
// The token is applied for consistency with the API call (GitHub release assets
// redirect to a pre-signed S3 URL; the token is dropped on redirect, which is
// the expected public-CDN flow).
func downloadAsset(ctx context.Context, url, token string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/octet-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := githubClient(ctx).Do(req)
	if err != nil {
		return fmt.Errorf("update: download asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: asset download returned %s", resp.Status)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return fmt.Errorf("update: read asset body: %w", err)
	}
	return nil
}
