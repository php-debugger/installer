package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/php-debugger/installer/internal/platform"
)

// loadFixture reads the recorded latest-release payload.
func loadFixture(t *testing.T) *Release {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "latest.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var rel Release
	if err := json.Unmarshal(data, &rel); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return &rel
}

func TestParseAssetName(t *testing.T) {
	tests := []struct {
		name string
		want ParsedAsset
		ok   bool
	}{
		{
			name: "php-php8.3-nts-linux-x86_64",
			want: ParsedAsset{Interpreter, "8.3", false, platform.Linux, platform.X8664},
			ok:   true,
		},
		{
			name: "php-debugger-php8.3-ts-macos-arm64.so",
			want: ParsedAsset{Extension, "8.3", true, platform.MacOS, platform.Arm64},
			ok:   true,
		},
		{
			name: "php-debugger-php8.3-nts-windows-x86_64.exe",
			want: ParsedAsset{Interpreter, "8.3", false, platform.Windows, platform.X8664},
			ok:   true,
		},
		{
			name: "php-debugger-php8.3-nts-windows-x86_64.dll",
			want: ParsedAsset{Extension, "8.3", false, platform.Windows, platform.X8664},
			ok:   true,
		},
		{
			// tolerant of an extra marker token
			name: "php-debugger-interp-php8.2-ts-linux-arm64",
			want: ParsedAsset{Interpreter, "8.2", true, platform.Linux, platform.Arm64},
			ok:   true,
		},
		{"checksums.txt", ParsedAsset{}, false},
		{"php-debugger-php8.3-nts-linux", ParsedAsset{}, false},    // no arch
		{"php-debugger-nts-linux-x86_64", ParsedAsset{}, false},    // no version
		{"php-debugger-php8.3-linux-x86_64", ParsedAsset{}, false}, // no threading
		{"", ParsedAsset{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseAssetName(tt.name)
			if ok != tt.ok {
				t.Fatalf("ParseAssetName(%q) ok = %v, want %v", tt.name, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ParseAssetName(%q) = %+v, want %+v", tt.name, got, tt.want)
			}
		})
	}
}

func TestSelectAsset(t *testing.T) {
	rel := loadFixture(t)

	tests := []struct {
		name     string
		sel      Selector
		wantName string
	}{
		{
			name:     "interpreter exact series",
			sel:      Selector{Interpreter, "8.3", false, platform.Linux, platform.X8664},
			wantName: "php-php8.3-nts-linux-x86_64",
		},
		{
			name:     "interpreter latest series when empty",
			sel:      Selector{Interpreter, "", false, platform.Linux, platform.X8664},
			wantName: "php-php8.4-nts-linux-x86_64",
		},
		{
			name:     "interpreter zts",
			sel:      Selector{Interpreter, "8.3", true, platform.Linux, platform.X8664},
			wantName: "php-php8.3-ts-linux-x86_64",
		},
		{
			name:     "interpreter macos arm64",
			sel:      Selector{Interpreter, "8.3", false, platform.MacOS, platform.Arm64},
			wantName: "php-php8.3-nts-macos-arm64",
		},
		{
			name:     "interpreter windows exe",
			sel:      Selector{Interpreter, "8.3", false, platform.Windows, platform.X8664},
			wantName: "php-php8.3-nts-windows-x86_64.exe",
		},
		{
			name:     "extension so",
			sel:      Selector{Extension, "8.3", false, platform.Linux, platform.X8664},
			wantName: "php-debugger-php8.3-nts-linux-x86_64.so",
		},
		{
			name:     "extension dll",
			sel:      Selector{Extension, "8.3", false, platform.Windows, platform.X8664},
			wantName: "php-debugger-php8.3-nts-windows-x86_64.dll",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectAsset(rel.Assets, tt.sel)
			if err != nil {
				t.Fatalf("SelectAsset: %v", err)
			}
			if got.Name != tt.wantName {
				t.Errorf("SelectAsset = %q, want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestSelectAssetNoMatch(t *testing.T) {
	rel := loadFixture(t)

	// arm64 windows interpreter does not exist in the fixture.
	_, err := SelectAsset(rel.Assets, Selector{Interpreter, "8.3", false, platform.Windows, platform.Arm64})
	var nme *NoMatchError
	if !errors.As(err, &nme) {
		t.Fatalf("expected *NoMatchError, got %v", err)
	}
	if len(nme.Available) == 0 {
		t.Error("NoMatchError should list available interpreter variants")
	}

	// an unknown series
	if _, err := SelectAsset(rel.Assets, Selector{Interpreter, "9.9", false, platform.Linux, platform.X8664}); err == nil {
		t.Error("expected error for unknown series")
	}
}

func TestLatestSeries(t *testing.T) {
	rel := loadFixture(t)

	got, err := LatestSeries(rel.Assets, Interpreter, false, platform.Linux, platform.X8664)
	if err != nil {
		t.Fatalf("LatestSeries: %v", err)
	}
	if got != "8.4" {
		t.Errorf("LatestSeries = %q, want 8.4", got)
	}

	// no ts interpreter for macos/x86_64 in fixture
	if _, err := LatestSeries(rel.Assets, Interpreter, true, platform.MacOS, platform.X8664); err == nil {
		t.Error("expected error when no variant matches")
	}
}

func TestCompareSeries(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"8.3", "8.2", 1},
		{"8.2", "8.3", -1},
		{"8.3", "8.3", 0},
		{"8.10", "8.9", 1},
		{"8.3.1", "8.3", 1},
		{"8.3", "8.3.0", 0},
	}
	for _, tt := range tests {
		if got := compareSeries(tt.a, tt.b); got != tt.want {
			t.Errorf("compareSeries(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestAvailableVariantsDeduped(t *testing.T) {
	rel := loadFixture(t)
	got := availableVariants(rel.Assets, Extension)
	// extensions in fixture: 8.3 nts linux/x86_64, 8.3 ts macos/arm64,
	// 8.3 nts windows/x86_64, 8.2 nts linux/x86_64
	want := []string{
		"php8.2 nts linux/x86_64",
		"php8.3 nts linux/x86_64",
		"php8.3 nts windows/x86_64",
		"php8.3 ts macos/arm64",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("availableVariants(Extension) = %v, want %v", got, want)
	}
}
