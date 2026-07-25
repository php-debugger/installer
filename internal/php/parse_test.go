package php

import (
	"reflect"
	"testing"
)

func TestParseKeyVals(t *testing.T) {
	out := "version=8.3.10\nseries=8.3\nzts=0\nextension_dir=/usr/lib/php/20230831\n"
	kv := parseKeyVals(out)
	want := map[string]string{
		"version":       "8.3.10",
		"series":        "8.3",
		"zts":           "0",
		"extension_dir": "/usr/lib/php/20230831",
	}
	if !reflect.DeepEqual(kv, want) {
		t.Errorf("parseKeyVals = %v, want %v", kv, want)
	}
}

func TestParseKeyValsEmptyExtensionDir(t *testing.T) {
	kv := parseKeyVals("version=8.2.1\nseries=8.2\nzts=1\nextension_dir=\n")
	if kv["extension_dir"] != "" {
		t.Errorf("empty extension_dir = %q, want empty", kv["extension_dir"])
	}
	if kv["zts"] != "1" {
		t.Errorf("zts = %q, want 1", kv["zts"])
	}
}

func TestParseIniOutputTypical(t *testing.T) {
	out := `Configuration File (php.ini) Path: /etc/php/8.3/cli
Loaded Configuration File:         /etc/php/8.3/cli/php.ini
Scan for additional .ini files in: /etc/php/8.3/cli/conf.d
Additional .ini files parsed:      /etc/php/8.3/cli/conf.d/10-opcache.ini,
/etc/php/8.3/cli/conf.d/20-xdebug.ini,
/etc/php/8.3/cli/conf.d/30-mysqli.ini
`
	got := parseIniOutput(out)
	want := IniPaths{
		LoadedFile: "/etc/php/8.3/cli/php.ini",
		ScanDir:    "/etc/php/8.3/cli/conf.d",
		AdditionalFiles: []string{
			"/etc/php/8.3/cli/conf.d/10-opcache.ini",
			"/etc/php/8.3/cli/conf.d/20-xdebug.ini",
			"/etc/php/8.3/cli/conf.d/30-mysqli.ini",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseIniOutput =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseIniOutputNone(t *testing.T) {
	out := `Configuration File (php.ini) Path: /usr/local/etc/php
Loaded Configuration File:         (none)
Scan for additional .ini files in: (none)
Additional .ini files parsed:      (none)
`
	got := parseIniOutput(out)
	if got.LoadedFile != "" {
		t.Errorf("LoadedFile = %q, want empty", got.LoadedFile)
	}
	if got.ScanDir != "" {
		t.Errorf("ScanDir = %q, want empty", got.ScanDir)
	}
	if len(got.AdditionalFiles) != 0 {
		t.Errorf("AdditionalFiles = %v, want empty", got.AdditionalFiles)
	}
}

func TestParseIniOutputSingleAdditional(t *testing.T) {
	out := `Loaded Configuration File:         /etc/php.ini
Scan for additional .ini files in: /etc/php.d
Additional .ini files parsed:      /etc/php.d/20-xdebug.ini
`
	got := parseIniOutput(out)
	if len(got.AdditionalFiles) != 1 || got.AdditionalFiles[0] != "/etc/php.d/20-xdebug.ini" {
		t.Errorf("AdditionalFiles = %v", got.AdditionalFiles)
	}
}

func TestParseModules(t *testing.T) {
	out := `[PHP Modules]
Core
date
json
mysqli
xdebug

[Zend Modules]
Xdebug
Zend OPcache
`
	got := parseModules(out)
	want := []string{"Core", "date", "json", "mysqli", "xdebug", "Xdebug", "Zend OPcache"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseModules = %v, want %v", got, want)
	}
}

func TestParseVersionLine(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantVersion string
		wantZTS     bool
		wantOK      bool
	}{
		{
			name:        "nts cli",
			in:          "PHP 8.3.10 (cli) (built: Jul  1 2024 10:00:00) (NTS)\nCopyright (c) The PHP Group",
			wantVersion: "8.3.10",
			wantZTS:     false,
			wantOK:      true,
		},
		{
			name:        "zts",
			in:          "PHP 8.2.20 (cli) (built: Jun  1 2024) (ZTS)",
			wantVersion: "8.2.20",
			wantZTS:     true,
			wantOK:      true,
		},
		{
			name:   "garbage",
			in:     "not php output",
			wantOK: false,
		},
		{
			name:   "empty",
			in:     "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, zts, ok := parseVersionLine(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && (v != tt.wantVersion || zts != tt.wantZTS) {
				t.Errorf("got (%q, %v), want (%q, %v)", v, zts, tt.wantVersion, tt.wantZTS)
			}
		})
	}
}
