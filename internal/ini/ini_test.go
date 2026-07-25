package ini

import (
	"reflect"
	"testing"
)

func TestStripXdebugLoaders(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		want        string
		wantRemoved []string
	}{
		{
			name:        "simple zend_extension",
			in:          "zend_extension=xdebug.so\n",
			want:        "",
			wantRemoved: []string{"zend_extension=xdebug.so"},
		},
		{
			name:        "plain extension= is left alone (xdebug loads via zend_extension only)",
			in:          "extension=xdebug\n",
			want:        "extension=xdebug\n",
			wantRemoved: nil,
		},
		{
			name:        "spaces and quotes and full path",
			in:          "zend_extension = \"/usr/lib/php/20210902/xdebug.so\"\n",
			want:        "",
			wantRemoved: []string{"zend_extension = \"/usr/lib/php/20210902/xdebug.so\""},
		},
		{
			name:        "commented loader is also removed",
			in:          ";zend_extension=xdebug.so\n",
			want:        "",
			wantRemoved: []string{";zend_extension=xdebug.so"},
		},
		{
			name:        "keeps other extensions and comments",
			in:          "extension=mysqli\n; xdebug is nice\nzend_extension=opcache.so\n",
			want:        "extension=mysqli\n; xdebug is nice\nzend_extension=opcache.so\n",
			wantRemoved: nil,
		},
		{
			name:        "removes only the loader among other lines",
			in:          "memory_limit=256M\nzend_extension=xdebug.so\ndisplay_errors=On\n",
			want:        "memory_limit=256M\ndisplay_errors=On\n",
			wantRemoved: []string{"zend_extension=xdebug.so"},
		},
		{
			name:        "preserves CRLF endings",
			in:          "memory_limit=256M\r\nzend_extension=xdebug.so\r\ndisplay_errors=On\r\n",
			want:        "memory_limit=256M\r\ndisplay_errors=On\r\n",
			wantRemoved: []string{"zend_extension=xdebug.so"},
		},
		{
			name:        "no trailing newline",
			in:          "a=1\nzend_extension=xdebug.so",
			want:        "a=1",
			wantRemoved: []string{"zend_extension=xdebug.so"},
		},
		{
			name:        "empty content",
			in:          "",
			want:        "",
			wantRemoved: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed := StripXdebugLoaders(tt.in)
			if got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(removed, tt.wantRemoved) {
				t.Errorf("removed = %#v, want %#v", removed, tt.wantRemoved)
			}
		})
	}
}

func TestCommentExtensionLoaders(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		want          string
		wantCommented []string
	}{
		{
			name:          "comments active extension and zend_extension",
			in:            "extension=mysqli.so\nzend_extension=/opt/opcache.so\nmemory_limit=256M\n",
			want:          "; extension=mysqli.so\n; zend_extension=/opt/opcache.so\nmemory_limit=256M\n",
			wantCommented: []string{"extension=mysqli.so", "zend_extension=/opt/opcache.so"},
		},
		{
			name:          "leaves already-commented and non-loaders alone",
			in:            ";extension=foo.so\ndisplay_errors=On\n",
			want:          ";extension=foo.so\ndisplay_errors=On\n",
			wantCommented: nil,
		},
		{
			name:          "preserves CRLF",
			in:            "extension=mysqli.so\r\nmemory_limit=256M\r\n",
			want:          "; extension=mysqli.so\r\nmemory_limit=256M\r\n",
			wantCommented: []string{"extension=mysqli.so"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, commented := CommentExtensionLoaders(tt.in)
			if got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(commented, tt.wantCommented) {
				t.Errorf("commented = %#v, want %#v", commented, tt.wantCommented)
			}
		})
	}
}

func TestDisallowedModes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none present", "memory_limit=256M\n", nil},
		{"only allowed", "xdebug.mode=debug\n", nil},
		{"off and debug", "xdebug.mode = off,debug\n", nil},
		{"single disallowed", "xdebug.mode=develop\n", []string{"develop"}},
		{"mixed", "xdebug.mode=debug,develop,coverage\n", []string{"develop", "coverage"}},
		{"quoted and spaced", "xdebug.mode = \"debug, profile \"\n", []string{"profile"}},
		{"commented line included", ";xdebug.mode=develop\n", []string{"develop"}},
		{"dedup across lines", "xdebug.mode=develop\nxdebug.mode=develop,trace\n", []string{"develop", "trace"}},
		{"case-insensitive tokens", "xdebug.mode=DEBUG,Develop\n", []string{"develop"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisallowedModes(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DisallowedModes = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSanitizeXdebugMode(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		want        string
		wantRemoved []string
		wantChanged bool
	}{
		{
			name:        "drops disallowed keeps allowed",
			in:          "xdebug.mode=debug,develop,coverage\n",
			want:        "xdebug.mode=debug\n",
			wantRemoved: []string{"develop", "coverage"},
			wantChanged: true,
		},
		{
			name:        "no allowed left falls back to off",
			in:          "xdebug.mode=develop,coverage\n",
			want:        "xdebug.mode=off\n",
			wantRemoved: []string{"develop", "coverage"},
			wantChanged: true,
		},
		{
			name:        "preserves spacing around equals",
			in:          "xdebug.mode = debug , trace\n",
			want:        "xdebug.mode = debug\n",
			wantRemoved: []string{"trace"},
			wantChanged: true,
		},
		{
			name:        "already valid untouched",
			in:          "xdebug.mode=off,debug\n",
			want:        "xdebug.mode=off,debug\n",
			wantRemoved: nil,
			wantChanged: false,
		},
		{
			name:        "commented line sanitized keeping marker",
			in:          ";xdebug.mode=develop\n",
			want:        ";xdebug.mode=off\n",
			wantRemoved: []string{"develop"},
			wantChanged: true,
		},
		{
			name:        "commented line preserves marker spacing",
			in:          "; xdebug.mode = debug, coverage\n",
			want:        "; xdebug.mode = debug\n",
			wantRemoved: []string{"coverage"},
			wantChanged: true,
		},
		{
			name:        "commented line already valid untouched",
			in:          ";xdebug.mode=off\n",
			want:        ";xdebug.mode=off\n",
			wantRemoved: nil,
			wantChanged: false,
		},
		{
			name:        "preserves CRLF",
			in:          "xdebug.mode=debug,profile\r\n",
			want:        "xdebug.mode=debug\r\n",
			wantRemoved: []string{"profile"},
			wantChanged: true,
		},
		{
			name:        "only touches the mode line",
			in:          "memory_limit=256M\nxdebug.mode=develop\ndisplay_errors=On\n",
			want:        "memory_limit=256M\nxdebug.mode=off\ndisplay_errors=On\n",
			wantRemoved: []string{"develop"},
			wantChanged: true,
		},
		{
			name:        "multiple mode lines",
			in:          "xdebug.mode=debug,develop\nxdebug.mode=trace\n",
			want:        "xdebug.mode=debug\nxdebug.mode=off\n",
			wantRemoved: []string{"develop", "trace"},
			wantChanged: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed, changed := SanitizeXdebugMode(tt.in)
			if got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(removed, tt.wantRemoved) {
				t.Errorf("removed = %#v, want %#v", removed, tt.wantRemoved)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

func TestParseDirective(t *testing.T) {
	tests := []struct {
		name          string
		line          string
		wantCommented bool
		wantKey       string
		wantValue     string
		wantOK        bool
	}{
		{"simple", "extension=mysqli", false, "extension", "mysqli", true},
		{"spaces", "  xdebug.mode  =  debug  ", false, "xdebug.mode", "debug", true},
		{"commented directive", ";extension=xdebug", true, "extension", "xdebug", true},
		{"lowercased key", "Zend_Extension=xdebug.so", false, "zend_extension", "xdebug.so", true},
		{"blank line", "", false, "", "", false},
		{"pure comment", "; just a note", true, "", "", false},
		{"section header", "[xdebug]", false, "", "", false},
		{"trailing cr", "extension=mysqli\r", false, "extension", "mysqli", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commented, key, value, ok := parseDirective(tt.line)
			if commented != tt.wantCommented || key != tt.wantKey || value != tt.wantValue || ok != tt.wantOK {
				t.Errorf("parseDirective(%q) = (%v, %q, %q, %v), want (%v, %q, %q, %v)",
					tt.line, commented, key, value, ok, tt.wantCommented, tt.wantKey, tt.wantValue, tt.wantOK)
			}
		})
	}
}
