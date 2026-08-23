package main

import "testing"

// TestFlagPreParseAcceptsBothSpellings is the regression for a bug that made
// `--log-format json` a silent no-op for every caller of this CLI.
//
// --log-level and --log-format are registered as ordinary cobra persistent
// flags, so pflag accepts both `--flag=value` and `--flag value`. But the
// logger has to exist before cobra parses anything, so main() pre-parses
// these two out of os.Args itself — and that pre-parse matched only the
// attached form. The separated form therefore parsed cleanly, produced no
// error, and had no effect at all: `pokkum build --log-format json` emitted
// text logs.
//
// It surfaced as empty digest/ref outputs from the GitHub Action, which
// passed --log-format json (separated) and then parsed the resulting stream
// for JSON keys that were never there. See Lessons.md, 2026-08-23.
func TestFlagPreParseAcceptsBothSpellings(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "attached form",
			args: []string{"pokkum", "build", "--log-format=json", "./app"},
			want: "json",
		},
		{
			// The regression. Every case below exists because this one shipped broken.
			name: "separated form",
			args: []string{"pokkum", "build", "--log-format", "json", "./app"},
			want: "json",
		},
		{
			name: "separated form after the positional argument, as the GitHub Action emits it",
			args: []string{"pokkum", "build", "--tag", "latest", "./app", "--log-format", "json"},
			want: "json",
		},
		{
			name: "absent falls back to the default",
			args: []string{"pokkum", "build", "./app"},
			want: "auto",
		},
		{
			// A trailing --log-format with no value must not consume the next
			// flag as its argument.
			name: "flag-shaped following argument is not consumed as a value",
			args: []string{"pokkum", "build", "--log-format", "--log-level", "debug"},
			want: "auto",
		},
		{
			// After a bare "--" everything is positional by convention, so a
			// project directory that happens to be named like a flag cannot
			// be reinterpreted as one.
			name: "double dash ends flag parsing",
			args: []string{"pokkum", "build", "--", "--log-format", "json"},
			want: "auto",
		},
		{
			name: "attached form wins over a later separated occurrence",
			args: []string{"pokkum", "--log-format=text", "build", "--log-format", "json"},
			want: "text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := flag(tc.args, "log-format", "auto"); got != tc.want {
				t.Errorf("flag(%q, \"log-format\", \"auto\") = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestFlagPreParseMatchesSetupLogger ties the pre-parse to the thing it
// feeds: a value that reaches flag() correctly but that setupLogger does not
// recognize would put the CLI right back to emitting the default format.
func TestFlagPreParseMatchesSetupLogger(t *testing.T) {
	got := flag([]string{"pokkum", "build", "--log-format", "json", "./app"}, "log-format", "auto")
	if logger := setupLogger("INFO", got); logger == nil {
		t.Fatalf("setupLogger(%q) returned nil", got)
	}
}
