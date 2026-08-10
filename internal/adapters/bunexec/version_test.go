package bunexec

import "testing"

func TestParseBunVersion(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    bunVersion
		wantErr bool
	}{
		{name: "plain", in: "1.3.14", want: bunVersion{1, 3, 14}},
		{name: "leading v", in: "v1.2.18", want: bunVersion{1, 2, 18}},
		{name: "trailing newline", in: "1.2.18\n", want: bunVersion{1, 2, 18}},
		{name: "trailing whitespace", in: "  1.2.18  ", want: bunVersion{1, 2, 18}},
		{name: "build metadata", in: "1.2.18+abcdef", want: bunVersion{1, 2, 18}},
		{name: "trailing garbage after version", in: "1.2.18 (abc123)", want: bunVersion{1, 2, 18}},
		{name: "no patch", in: "1.2", want: bunVersion{1, 2, 0}},
		{name: "canary suffix on patch", in: "1.2.18-canary.3", want: bunVersion{1, 2, 18}},
		{name: "zero patch explicit", in: "1.0.0", want: bunVersion{1, 0, 0}},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "single component", in: "1", wantErr: true},
		{name: "non numeric major", in: "a.2.18", wantErr: true},
		{name: "non numeric minor", in: "1.b.18", wantErr: true},
		{name: "non numeric patch", in: "1.2.c", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBunVersion(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseBunVersion(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBunVersion(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("parseBunVersion(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBunVersionString(t *testing.T) {
	v := bunVersion{1, 2, 18}
	if got, want := v.String(), "1.2.18"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestBunVersionLess(t *testing.T) {
	tests := []struct {
		name string
		a, b bunVersion
		want bool
	}{
		{"equal", bunVersion{1, 2, 18}, bunVersion{1, 2, 18}, false},
		{"lower patch", bunVersion{1, 2, 17}, bunVersion{1, 2, 18}, true},
		{"higher patch", bunVersion{1, 2, 19}, bunVersion{1, 2, 18}, false},
		{"lower minor", bunVersion{1, 1, 99}, bunVersion{1, 2, 0}, true},
		{"higher minor beats lower patch", bunVersion{1, 3, 0}, bunVersion{1, 2, 99}, false},
		{"lower major", bunVersion{0, 99, 99}, bunVersion{1, 0, 0}, true},
		{"higher major beats everything", bunVersion{2, 0, 0}, bunVersion{1, 99, 99}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.less(tt.b); got != tt.want {
				t.Errorf("(%s).less(%s) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDefaultMinBunVersionParses(t *testing.T) {
	// Guard against a typo in the constant itself: it must be a valid version
	// string, since Preflight parses it whenever no override is supplied.
	v, err := parseBunVersion(defaultMinBunVersion)
	if err != nil {
		t.Fatalf("defaultMinBunVersion %q does not parse: %v", defaultMinBunVersion, err)
	}
	if want := (bunVersion{1, 2, 18}); v != want {
		t.Errorf("defaultMinBunVersion parsed to %+v, want %+v", v, want)
	}
}
