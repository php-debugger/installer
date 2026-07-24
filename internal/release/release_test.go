package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLatestRelease(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}

	var gotPath, gotUA, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	c := NewClient()
	c.BaseURL = srv.URL
	c.Token = "secret-token"

	rel, err := c.LatestRelease(context.Background())
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel.TagName != "0.2.0" {
		t.Errorf("TagName = %q, want 0.2.0", rel.TagName)
	}
	if len(rel.Assets) != 13 {
		t.Errorf("got %d assets, want 13", len(rel.Assets))
	}
	if gotPath != "/repos/php-debugger/php-debugger/releases/latest" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotUA == "" {
		t.Error("User-Agent header must be set (GitHub rejects requests without one)")
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want Bearer secret-token", gotAuth)
	}
}

func TestLatestReleaseNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient()
	c.BaseURL = srv.URL
	c.Token = ""

	if _, err := c.LatestRelease(context.Background()); err == nil {
		t.Error("expected error on 404")
	}
}

func TestLatestReleaseNoTokenOmitsAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"tag_name":"0.0.1","assets":[]}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.BaseURL = srv.URL
	c.Token = ""

	if _, err := c.LatestRelease(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization should be empty without a token, got %q", gotAuth)
	}
}

func TestDownload(t *testing.T) {
	const payload = "fake-php-binary-contents"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer srv.Close()

	c := NewClient()
	destDir := filepath.Join(t.TempDir(), "downloads")
	asset := Asset{Name: "php-debugger-php8.3-nts-linux-x86_64", DownloadURL: srv.URL + "/dl/bin"}

	path, err := c.Download(context.Background(), asset, destDir)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if filepath.Base(path) != asset.Name {
		t.Errorf("downloaded file name = %q, want %q", filepath.Base(path), asset.Name)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("downloaded content = %q, want %q", got, payload)
	}
}

func TestDownloadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient()
	asset := Asset{Name: "x", DownloadURL: srv.URL}
	dest := filepath.Join(t.TempDir(), "d")
	if _, err := c.Download(context.Background(), asset, dest); err == nil {
		t.Error("expected error on 403 download")
	}
	// the partial file must not be left behind
	if _, err := os.Stat(filepath.Join(dest, "x")); !os.IsNotExist(err) {
		t.Error("failed download should not leave a file behind")
	}
}
