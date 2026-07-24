// Package release fetches the latest release from the php-debugger GitHub
// repository, selects the right asset for a given platform/PHP variant, and
// downloads it. Asset-name interpretation lives in naming.go — the single place
// that encodes the release naming convention.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultOwner and DefaultRepo identify the upstream repository.
	DefaultOwner = "php-debugger"
	DefaultRepo  = "php-debugger"

	defaultBaseURL   = "https://api.github.com"
	defaultUserAgent = "php-debugger-installer"
	apiVersion       = "2022-11-28"
)

// Release is the subset of a GitHub release we use.
type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	Assets  []Asset `json:"assets"`
}

// Asset is a downloadable file attached to a release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Client talks to the GitHub releases API.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	Owner     string
	Repo      string
	Token     string // optional; sent as a Bearer token when set
	UserAgent string
}

// NewClient returns a Client with sensible defaults, reading GITHUB_TOKEN from
// the environment if present (raising the API rate limit).
func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		BaseURL:   defaultBaseURL,
		Owner:     DefaultOwner,
		Repo:      DefaultRepo,
		Token:     os.Getenv("GITHUB_TOKEN"),
		UserAgent: defaultUserAgent,
	}
}

// LatestRelease fetches the repository's latest published release.
func (c *Client) LatestRelease(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.baseURL(), c.Owner, c.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent())
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("fetching latest release: unexpected status %s: %s",
			resp.Status, string(body))
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decoding latest release: %w", err)
	}
	return &rel, nil
}

// Download streams asset to a file inside destDir (created if needed) and returns
// the path to the downloaded file. The file is named after the asset.
func (c *Client) Download(ctx context.Context, asset Asset, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("creating download directory: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Accept", "application/octet-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: unexpected status %s", asset.Name, resp.Status)
	}

	dest := filepath.Join(destDir, asset.Name)
	f, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", dest, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(dest)
		return "", fmt.Errorf("writing %s: %w", dest, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("closing %s: %w", dest, err)
	}
	return dest, nil
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c *Client) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	return defaultUserAgent
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
