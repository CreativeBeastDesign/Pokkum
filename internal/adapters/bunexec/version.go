package bunexec

import (
	"fmt"
	"strconv"
	"strings"
)

// defaultMinBunVersion is the floor bunexec enforces when
// ports.PreflightRequest.MinBunVersion is empty. It matches
// @jesterkit/exe-sveltekit's own hard minimum: below this version Bun's
// `--compile` target list does not include every target the adapter and
// Pokkum rely on.
const defaultMinBunVersion = "1.2.18"

// bunVersion is a minimal MAJOR.MINOR.PATCH triple. Bun's own versioning is
// plain semver without pre-release or build-metadata segments in practice, so
// a hand-rolled comparator avoids pulling in a semver dependency for three
// integers.
type bunVersion struct {
	major, minor, patch int
}

// String renders the version in canonical MAJOR.MINOR.PATCH form.
func (v bunVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// less reports whether v is strictly older than o.
func (v bunVersion) less(o bunVersion) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

// parseBunVersion parses a version string such as "1.3.14", "v1.2.18", or the
// raw (possibly newline- or whitespace-terminated) output of `bun --version`.
// It tolerates a leading "v", trailing build metadata after a "+", trailing
// whitespace, and a patch component followed by non-digit characters (as in
// a canary build like "1.2.18-canary.3", which is treated as patch 18). A
// missing patch component defaults to 0.
func parseBunVersion(s string) (bunVersion, error) {
	orig := s
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, " \t\r\n"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return bunVersion{}, fmt.Errorf("bunexec: version %q: empty", orig)
	}

	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return bunVersion{}, fmt.Errorf("bunexec: version %q: want MAJOR.MINOR[.PATCH]", orig)
	}

	major, err := strconv.Atoi(numericPrefix(parts[0]))
	if err != nil {
		return bunVersion{}, fmt.Errorf("bunexec: version %q: bad major component: %w", orig, err)
	}
	minor, err := strconv.Atoi(numericPrefix(parts[1]))
	if err != nil {
		return bunVersion{}, fmt.Errorf("bunexec: version %q: bad minor component: %w", orig, err)
	}

	patch := 0
	if len(parts) == 3 {
		p := numericPrefix(parts[2])
		if p == "" {
			return bunVersion{}, fmt.Errorf("bunexec: version %q: bad patch component", orig)
		}
		patch, err = strconv.Atoi(p)
		if err != nil {
			return bunVersion{}, fmt.Errorf("bunexec: version %q: bad patch component: %w", orig, err)
		}
	}

	return bunVersion{major: major, minor: minor, patch: patch}, nil
}

// numericPrefix returns the leading run of ASCII digits in s, e.g.
// "18-canary.3" -> "18". An input with no leading digit yields "".
func numericPrefix(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
