package changelog

import "testing"

const sample = `# Changelog

Intro.

## [0.9.0] - 2026-12-01

- nine

## v0.8.1

- eight one

## 0.8.0

### Added

- eight
`

func TestChangelogSection(t *testing.T) {
	for _, tc := range []struct{ version, want string }{
		{"0.9.0", "- nine"},
		{"0.8.1", "- eight one"},
		{"v0.8.1", "- eight one"},
		{"0.8.0", "### Added\n\n- eight"},
		{"0.8", ""},
		{"0.7.0", ""},
		{"", ""},
	} {
		if got := Section(sample, tc.version); got != tc.want {
			t.Errorf("Section(%q) = %q, want %q", tc.version, got, tc.want)
		}
	}
	if got := Section("", "0.8.0"); got != "" {
		t.Errorf("Section of nothing = %q", got)
	}
	if got := Section("## 1.0.0\r\n\r\n- crlf\r\n", "1.0.0"); got != "- crlf" {
		t.Errorf("CRLF section = %q", got)
	}
}

func TestChangelogNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"0.8.0", "0.7.3", true},
		{"0.7.3", "0.8.0", false},
		{"0.8.0", "0.8.0", false},
		{"v0.10.0", "0.9.9", true},
		{"0.8", "0.8.0", false},
		{"0.8.1", "0.8", true},
		{"0.8.0", "0.8.0-rc.1", true},
		{"0.8.0-rc.1", "0.8.0", false},
		{"0.8.0-rc.1", "0.7.9", true},
		{"0.x.1", "0.0.0", true},
		{"0.x.0", "0.0.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
