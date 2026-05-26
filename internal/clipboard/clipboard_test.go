package clipboard

import "testing"

func TestCanonicalSelectionName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", SelectionClipboard},
		{"clipboard", SelectionClipboard},
		{"CLIPBOARD", SelectionClipboard},
		{"primary", SelectionPrimary},
		{"PRIMARY", SelectionPrimary},
		{"both", SelectionBoth},
		{"BoTh", SelectionBoth},
		{"weird", "weird"},
	}
	for _, tc := range tests {
		if got := canonicalSelectionName(tc.in); got != tc.want {
			t.Fatalf("canonicalSelectionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalMime(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", MimeTextPlain},
		{"text/plain", MimeTextPlain},
		{"TEXT/PLAIN", MimeTextPlain},
		{"text/plain;charset=utf-8", MimeTextPlain},
		{"text/uri-list", MimeTextURIList},
		{"application/octet-stream", "application/octet-stream"},
	}
	for _, tc := range tests {
		if got := canonicalMime(tc.in); got != tc.want {
			t.Fatalf("canonicalMime(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
