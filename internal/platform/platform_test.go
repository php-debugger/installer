package platform

import "testing"

func TestDetectOS(t *testing.T) {
	tests := []struct {
		goos    string
		want    OS
		wantErr bool
	}{
		{"linux", Linux, false},
		{"darwin", MacOS, false},
		{"windows", Windows, false},
		{"freebsd", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := DetectOS(tt.goos)
		if tt.wantErr {
			if err == nil {
				t.Errorf("DetectOS(%q): expected error, got %q", tt.goos, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("DetectOS(%q): unexpected error: %v", tt.goos, err)
		}
		if got != tt.want {
			t.Errorf("DetectOS(%q) = %q, want %q", tt.goos, got, tt.want)
		}
	}
}

func TestDetectArch(t *testing.T) {
	tests := []struct {
		goarch  string
		want    Arch
		wantErr bool
	}{
		{"amd64", X8664, false},
		{"arm64", Arm64, false},
		{"386", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := DetectArch(tt.goarch)
		if tt.wantErr {
			if err == nil {
				t.Errorf("DetectArch(%q): expected error, got %q", tt.goarch, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("DetectArch(%q): unexpected error: %v", tt.goarch, err)
		}
		if got != tt.want {
			t.Errorf("DetectArch(%q) = %q, want %q", tt.goarch, got, tt.want)
		}
	}
}

func TestDetectFor(t *testing.T) {
	got, err := DetectFor("darwin", "arm64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.OS != MacOS || got.Arch != Arm64 {
		t.Errorf("DetectFor(darwin, arm64) = %v, want macos/arm64", got)
	}
	if _, err := DetectFor("plan9", "amd64"); err == nil {
		t.Error("DetectFor(plan9, amd64): expected error")
	}
	if _, err := DetectFor("linux", "mips"); err == nil {
		t.Error("DetectFor(linux, mips): expected error")
	}
}
