package bunexec

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// buildEnv merges the inherited process environment with extra KEY=VALUE
// entries, extra winning on collision. It never mutates os.Environ() or
// extra; it returns a freshly built, sorted slice so that the resulting
// subprocess environment (and therefore anything that hashes or logs it) is
// deterministic across runs with the same inputs.
func buildEnv(extra []string) []string {
	m := make(map[string]string, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return sortedEnvSlice(m)
}

// buildEnvWithEpoch is buildEnv plus SOURCE_DATE_EPOCH set from
// sourceDateEpoch, always taking precedence over any same-named entry in
// extra. Prepare and Compile use this; Preflight (which has no
// SourceDateEpoch to export) uses buildEnv directly.
func buildEnvWithEpoch(extra []string, sourceDateEpoch time.Time) []string {
	m := make(map[string]string, len(os.Environ())+len(extra)+1)
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	m["SOURCE_DATE_EPOCH"] = strconv.FormatInt(sourceDateEpoch.Unix(), 10)
	return sortedEnvSlice(m)
}

func sortedEnvSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
