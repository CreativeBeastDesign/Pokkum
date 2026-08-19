package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
)

// findingHeaderRe matches overnight-findings.md's numbered entry headers,
// e.g. "### 14. `cosign verify-attestation` rejects Pokkum's DSSE
// attestation in tag-fallback mode".
var findingHeaderRe = regexp.MustCompile(`^### (\d+)\.`)

// ParseFindingsNumbers scans path (overnight-findings.md) for its numbered
// "### N." entry headers and returns the set of entry numbers that actually
// exist, so an item's evidence.findings list can be validated against real
// entries instead of trusting arbitrary integers.
func ParseFindingsNumbers(path string) (map[int]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gen-docs: reading %s: %w", path, err)
	}
	defer f.Close()

	nums := map[int]bool{}
	scanner := bufio.NewScanner(f)
	// overnight-findings.md is prose with long paragraphs, not
	// line-oriented data — bump past bufio.Scanner's 64KB default token
	// limit so a long paragraph line can never make this silently stop
	// scanning partway through the file (mem:self_review_checklist row 26).
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		m := findingHeaderRe.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		nums[n] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gen-docs: scanning %s: %w", path, err)
	}
	return nums, nil
}
