package platform

import "testing"

func TestDetectNativeArch(t *testing.T) {
	// fakeSysctl returns a fixed hw.optional.arm64 result.
	fakeSysctl := func(val int, ok bool) func(string) (int, bool) {
		return func(name string) (int, bool) {
			if name == "hw.optional.arm64" {
				return val, ok
			}
			return 0, false
		}
	}

	tests := []struct {
		name   string
		os     OS
		arch   Arch
		sysctl func(string) (int, bool)
		want   Arch
	}{
		{
			name:   "amd64 build on apple silicon upgrades to arm64",
			os:     MacOS,
			arch:   X8664,
			sysctl: fakeSysctl(1, true),
			want:   Arm64,
		},
		{
			name:   "amd64 build on intel mac stays amd64",
			os:     MacOS,
			arch:   X8664,
			sysctl: fakeSysctl(0, false), // sysctl key absent on Intel
			want:   X8664,
		},
		{
			name:   "arm64 build on apple silicon unchanged (sysctl not consulted)",
			os:     MacOS,
			arch:   Arm64,
			sysctl: func(string) (int, bool) { t.Fatal("sysctl should not be called"); return 0, false },
			want:   Arm64,
		},
		{
			name:   "linux amd64 unchanged",
			os:     Linux,
			arch:   X8664,
			sysctl: func(string) (int, bool) { t.Fatal("sysctl should not be called"); return 0, false },
			want:   X8664,
		},
		{
			name:   "windows amd64 unchanged",
			os:     Windows,
			arch:   X8664,
			sysctl: func(string) (int, bool) { t.Fatal("sysctl should not be called"); return 0, false },
			want:   X8664,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectNativeArch(tt.os, tt.arch, tt.sysctl); got != tt.want {
				t.Errorf("detectNativeArch(%s, %s) = %s, want %s", tt.os, tt.arch, got, tt.want)
			}
		})
	}
}
