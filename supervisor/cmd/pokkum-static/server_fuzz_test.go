package main

import (
	"strings"
	"testing"
)

// FuzzParseSingleRange exercises parseSingleRange against hostile Range
// header values. This code is network-reachable inside every shipped static
// image with no WAF in front — the Range header comes straight off an
// incoming HTTP request. The property under test is NOT "no error" (a
// malformed/unsatisfiable header is an entirely expected outcome, handled by
// the caller) but that parseSingleRange never panics and NEVER returns an
// offset pair outside the valid [0, size) byte range it promises in its own
// doc comment — an out-of-range result here would let a caller read past
// EOF, seek negative, or otherwise misbehave downstream in serveFile's
// io.CopyN/Seek calls.
func FuzzParseSingleRange(f *testing.F) {
	sizes := []int64{0, 1, 2, 100, 1024, 1 << 20}
	headers := []string{
		"",
		"bytes=0-499",
		"bytes=500-999",
		"bytes=-500",
		"bytes=9500-",
		"bytes=0-0",
		"bytes=0-",
		"bytes=-0",
		"bytes=0-499,500-999", // multi-range: must be ignored (satisfiable=false)
		"bytes=abc-def",       // malformed
		"bytes=",
		"bytes=-",
		"byte=0-499",                   // wrong unit
		"BYTES=0-499",                  // wrong case
		"bytes=0-99999999999999999999", // overflow
		"bytes=-99999999999999999999",  // overflow suffix
		"bytes=-1-10",                  // double-negative-ish
		"bytes=10--5",
		"bytes=999999999999-1000000000000",
		"bytes=0-499 ",
		"bytes= 0 - 499 ",
		"bytes=\x00-10",
		strings.Repeat("bytes=0-499,", 500) + "0-1", // huge multi-range list
	}
	for _, h := range headers {
		for _, sz := range sizes {
			f.Add(h, sz)
		}
	}

	f.Fuzz(func(t *testing.T, header string, size int64) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseSingleRange(%q, %d) panicked: %v", header, size, r)
			}
		}()

		rng, satisfiable, unsatisfiable := parseSingleRange(header, size)

		if satisfiable && unsatisfiable {
			t.Fatalf("parseSingleRange(%q, %d) = satisfiable AND unsatisfiable, contract requires exactly one or neither", header, size)
		}

		if satisfiable {
			start, end := rng[0], rng[1]
			if size <= 0 {
				t.Fatalf("parseSingleRange(%q, %d) reported satisfiable for non-positive size, range [%d,%d]", header, size, start, end)
			}
			if start < 0 {
				t.Fatalf("parseSingleRange(%q, %d) = start %d < 0", header, size, start)
			}
			if end >= size {
				t.Fatalf("parseSingleRange(%q, %d) = end %d >= size %d, out of [0,size) contract", header, size, end, size)
			}
			if start > end {
				t.Fatalf("parseSingleRange(%q, %d) = start %d > end %d", header, size, start, end)
			}
		}
	})
}

// FuzzParseAcceptEncoding exercises parseAcceptEncoding against hostile
// Accept-Encoding header values (also client-controlled, also
// network-reachable in every shipped static image). Only asserts no panic
// and no obviously-malformed output (a non-empty token never maps through
// to an empty-string key), since the function's whole contract is "best
// effort negotiation, defaults to identity on anything it can't parse".
func FuzzParseAcceptEncoding(f *testing.F) {
	f.Add("gzip")
	f.Add("br;q=1.0, gzip;q=0.8, *;q=0.1")
	f.Add("gzip;q=0")
	f.Add("")
	f.Add(",,,")
	f.Add("gzip;q=")
	f.Add("gzip;q=abc")
	f.Add("gzip;q=-1")
	f.Add("gzip;q=99999999999999999999")
	f.Add(strings.Repeat("gzip,", 10000))
	f.Add("\x00\x01\x02")
	f.Add("GZIP;Q=1")

	f.Fuzz(func(t *testing.T, header string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseAcceptEncoding(%q) panicked: %v", header, r)
			}
		}()
		accepted := parseAcceptEncoding(header)
		for token := range accepted {
			if token == "" {
				t.Fatalf("parseAcceptEncoding(%q) produced an empty-string token key", header)
			}
			if token != strings.ToLower(token) {
				t.Fatalf("parseAcceptEncoding(%q) produced non-lowercased token %q", header, token)
			}
		}
	})
}

// FuzzIfRangeMatches exercises ifRangeMatches, which gates whether a Range
// request is honoured (206) or falls back to a full response (200) —
// getting this wrong in either direction is a correctness bug (serving a
// stale partial range, or refusing an otherwise-valid range), though not by
// itself a memory-safety concern. Only asserts no panic here, since
// ifRangeMatches has no numeric contract to violate the way
// parseSingleRange does.
func FuzzIfRangeMatches(f *testing.F) {
	etags := []string{"", "abc123", strings.Repeat("f", 64)}
	ifRanges := []string{
		"",
		`"abc123"`,
		`W/"abc123"`,
		"abc123",
		"   ",
		`"`,
		`W/`,
		"\x00",
		strings.Repeat("a", 10000),
	}
	for _, ir := range ifRanges {
		for _, et := range etags {
			f.Add(ir, et)
		}
	}

	f.Fuzz(func(t *testing.T, ifRange, etag string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ifRangeMatches(%q, %q) panicked: %v", ifRange, etag, r)
			}
		}()
		_ = ifRangeMatches(ifRange, etag)
	})
}
