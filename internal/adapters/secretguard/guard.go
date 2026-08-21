package secretguard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// defaultMaxFileSizeBytes is the fallback per-file text-scan size ceiling
// when ports.SecretScanRequest.MaxFileSizeBytes is zero. 16MiB comfortably
// covers a single minified/bundled JS or CSS chunk from a real SvelteKit
// build — the previous 2MiB ceiling was sized for hand-written source
// files and silently skipped (with the match-discarding bug fixed below,
// would have silently PASSED) exactly the minified server/client bundles
// this adapter most needs to cover once it scans build OUTPUT rather than
// only source.
const defaultMaxFileSizeBytes = 16 * 1024 * 1024

// binarySniffBytes is how much of a file's head is read to decide whether
// it looks like binary content, regardless of the file's total size. This
// keeps the "is this even text" check cheap for large files (a multi-GB
// binary asset costs one bounded read, not a full load).
const binarySniffBytes = 512

type rule struct {
	Name    string
	Pattern *regexp.Regexp
	// Validate, when non-nil, is an extra check run against the exact
	// substring Pattern matched, before it is reported as a finding. Every
	// rule below except the JWT one leaves this nil and relies on Pattern
	// alone — a high-signal literal prefix (ghp_, xoxb-, sk_live_, ...) is
	// already near-zero false positive on its own. JWT is the one shape
	// where the regex alone (three dot-separated base64url runs starting
	// with the base64 encoding of `{"`) is not tight enough by itself, so
	// looksLikeJWTHeader below decodes the first segment and confirms it is
	// actually JSON containing "alg" before the match counts.
	Validate func(match string) bool
}

var defaultSecretRules = []rule{
	{
		Name:    "RSA Private Key",
		Pattern: regexp.MustCompile(`-----BEGIN (?:RSA )?PRIVATE KEY-----`),
	},
	{
		Name:    "AWS Access Key ID",
		Pattern: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	},
	{
		Name: "GitHub Personal Access Token",
		// Classic GitHub PATs are `ghp_` + 36 alphanumeric chars, but
		// fine-grained PATs use the same prefix with a much longer,
		// variable-length suffix — an exact {36} quantifier missed both
		// the fine-grained shape AND, per the F9 field test, any token
		// whose length simply wasn't exactly 36 for whatever reason (the
		// \b on both sides required an exact-length match with no partial
		// credit). {36,255} keeps the same near-zero-false-positive prefix
		// while accepting the real range of lengths GitHub actually issues.
		Pattern: regexp.MustCompile(`\bghp_[a-zA-Z0-9]{36,255}\b`),
	},
	{
		Name: "GitHub App Token",
		// gho_ (OAuth), ghu_ (user-to-server), ghs_ (server-to-server) share
		// the same value shape as ghp_ but are not personal access tokens,
		// so they get their own rule name — a finding should say which kind
		// of GitHub credential was found, not lump every gh*_ prefix under
		// "personal access token".
		Pattern: regexp.MustCompile(`\bgh[osu]_[a-zA-Z0-9]{36,255}\b`),
	},
	{
		Name: "Slack Token",
		// xoxb- (bot) / xoxp- (user) tokens: <workspace-id>-<id>-<secret>,
		// with the user token format sometimes carrying a fourth numeric
		// segment. The xoxb-/xoxp- literal prefix is essentially never seen
		// outside a real Slack token, so this stays high-signal even with a
		// fairly permissive tail.
		Pattern: regexp.MustCompile(`\bxox[bp]-[0-9]+-[0-9]+-(?:[0-9]+-)?[a-zA-Z0-9]+\b`),
	},
	{
		Name:    "Stripe Live Secret Key",
		Pattern: regexp.MustCompile(`\bsk_live_[a-zA-Z0-9]{16,99}\b`),
	},
	{
		Name:    "GitLab Personal Access Token",
		Pattern: regexp.MustCompile(`\bglpat-[a-zA-Z0-9_-]{20,50}\b`),
	},
	{
		Name: "JSON Web Token (JWT)",
		// `eyJ` is the base64 encoding of the literal `{"` almost every JWT
		// header starts with (`{"alg":...`), so requiring it plus two more
		// dot-separated base64url runs narrows the regex considerably on its
		// own — but base64 blobs unrelated to JWTs can still coincidentally
		// start that way, which is exactly the false-positive risk called
		// out for this format. Validate below closes that gap by actually
		// decoding the header and checking it is JSON with an "alg" key,
		// the one field RFC 7519 §5.1 guarantees every JWT header carries.
		Pattern:  regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{5,}\.[a-zA-Z0-9_-]{10,}\.[a-zA-Z0-9_-]{10,}`),
		Validate: looksLikeJWTHeader,
	},
	{
		Name:    "Google API Key",
		Pattern: regexp.MustCompile(`\bAIzaSy[a-zA-Z0-9_-]{33}\b`),
	},
	{
		Name: "Generic Hardcoded Password Assignment",
		// The value class deliberately excludes JS structural punctuation —
		// ( ) { } [ ] ; — on top of quotes and whitespace.
		//
		// Without that exclusion this rule fired on minified bundles constantly:
		// a reported false positive captured `,!!_),!_){this.error=` after a
		// `token:` key, which is code, not a credential. Any 8+ run of
		// non-quote, non-space bytes qualified, and minified output is full of
		// such runs.
		//
		// Brackets and semicolons are the discriminator rather than a stricter
		// alphanumeric class, because a real password legitimately contains
		// punctuation: `p@ss,w0rd!` must still match, while `){this.error=`
		// must not. Restricting the value to [A-Za-z0-9...] would trade this
		// false positive for false negatives on exactly the passwords the rule
		// exists to catch, which is the worse error for a secret scanner.
		//
		// The key side is suffix-anchored rather than exact-word: the old
		// `\b(?:password|secret|api_key|token)` required the key to START
		// with one of those words, so `fallbackPassword`, `apiToken`,
		// `DB_PASSWORD` and `stripeSecret` — the dominant camelCase / snake_case
		// / SCREAMING_SNAKE naming conventions in real JS/TS — never matched at
		// all (roadmap: generic-secret-rule-key-coverage). `[a-z0-9_]*` (folded
		// case-insensitive by the leading `(?i)`) absorbs any prefix, so the
		// keyword only has to appear at the END of the identifier, immediately
		// before the `[:=]`. That end-anchoring is also what keeps this safe
		// against the "stop word" false positives a naive `contains` match
		// would invite (`tokenizer`, `secretary`, `keyboard`, `monkey`): none
		// of those identifiers END in `token`/`secret`/`api_key`/`apikey`
		// immediately before an assignment operator, so a trailing-boundary
		// check is unnecessary — the `\s*[:=]` anchor IS the boundary. A key
		// where the keyword is a PREFIX rather than a suffix (`passwordHash`)
		// is deliberately still excluded: this rule only ever claimed to catch
		// identifiers that ARE a password/secret/token/api key, not every
		// identifier that mentions one.
		//
		// `api_?key` folds `api_key` and `apikey` into one alternative — `(?i)`
		// only case-folds letters, not the literal underscore, so the two
		// spellings need the explicit optional `_` rather than relying on the
		// flag.
		Pattern: regexp.MustCompile(`(?i)\b[a-z0-9_]*(?:password|secret|token|api_?key)\s*[:=]\s*["']([^"'\s(){}\[\];]{8,})["']`),
	},
}

// precompressedExts are sidecar files internal/adapters/precompressutils
// generates from an already-present sibling file (e.g. app.js.gz from
// app.js). They are binary-encoded duplicates of content that either
// already went through this same scan uncompressed, or (for a pre-build
// source scan) never exists at all — either way, scanning the compressed
// bytes as text is both wasted work and a guaranteed non-match, and
// treating a false negative there as if it says anything about the
// underlying content would be misleading. Listed explicitly (not left to
// the binary sniff alone) because gzip/brotli/zstd headers are short and
// a false "looks like text" read is more plausible on a few header bytes
// than on a larger file.
var precompressedExts = map[string]bool{
	".gz":  true,
	".br":  true,
	".zst": true,
}

var _ ports.SecretGuard = (*Adapter)(nil)

// Adapter implements ports.SecretGuard.
type Adapter struct{}

// NewAdapter constructs a new Adapter.
func NewAdapter() *Adapter {
	return &Adapter{}
}

// ScanDirectory walks req.ProjectDir and checks files for hardcoded secret
// leaks. A file that looks like text but exceeds the size ceiling is
// recorded in the result's Skipped list and forces Passed=false — this
// adapter never silently reports a clean scan for a file it did not
// actually read. A file that looks like binary content (a sniffed NUL
// byte, or a known precompressed-sidecar extension) is skipped silently:
// it was never scannable text in the first place, so there is no coverage
// gap to report.
func (a *Adapter) ScanDirectory(ctx context.Context, req ports.SecretScanRequest) (ports.SecretScanResult, error) {
	if req.ProjectDir == "" {
		return ports.SecretScanResult{Passed: true}, nil
	}

	maxSize := req.MaxFileSizeBytes
	if maxSize <= 0 {
		maxSize = defaultMaxFileSizeBytes
	}

	ignorer, err := loadIgnorer(req.ProjectDir, req.ScanSourcemaps)
	if err != nil {
		return ports.SecretScanResult{}, err
	}

	var compiledAllow []*regexp.Regexp
	for _, pat := range req.AllowPatterns {
		if strings.TrimSpace(pat) == "" {
			continue
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return ports.SecretScanResult{}, fmt.Errorf("secretguard: invalid allow pattern %q: %w", pat, err)
		}
		compiledAllow = append(compiledAllow, re)
	}

	var matches []ports.SecretMatch
	var skipped []ports.SecretSkip

	walkErr := filepath.WalkDir(req.ProjectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, err := filepath.Rel(req.ProjectDir, path)
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if rel != "." && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".svelte-kit" || d.Name() == ".pokkum") {
				return filepath.SkipDir
			}
			if rel != "." && ignorer != nil && ignorer.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}

		if ignorer != nil && ignorer.Match(rel, false) {
			return nil
		}
		if precompressedExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		fileMatches, skip, err := scanFile(path, rel, compiledAllow, maxSize)
		if err != nil {
			// Consistent with the pre-existing behavior: an unreadable file
			// (permissions, a symlink race, etc.) does not fail the whole
			// walk. Unlike the old code, this is the ONLY case that still
			// swallows an error silently — a genuine size-based skip goes
			// through the `skip != nil` branch below instead, precisely so
			// it cannot be confused with "scanned clean".
			return nil
		}
		if skip != nil {
			skipped = append(skipped, *skip)
			return nil
		}
		matches = append(matches, fileMatches...)
		return nil
	})

	if walkErr != nil {
		return ports.SecretScanResult{}, fmt.Errorf("secretguard: scan %s: %w", req.ProjectDir, walkErr)
	}

	return ports.SecretScanResult{
		Matches: matches,
		Skipped: skipped,
		Passed:  len(matches) == 0 && len(skipped) == 0,
	}, nil
}

// loadIgnorer builds the ignore matcher for req.ProjectDir. When
// scanSourcemaps is true, the "*.map" entry in ignoreutils.DefaultPatterns
// is neutralized with a trailing negation before the project's own
// .pokkumignore is layered on top, so a build that genuinely ships
// sourcemaps (--sourcemap) still has them covered, while the project's own
// .pokkumignore rules — evaluated last — can still re-exclude specific
// paths if the project owner wants that.
func loadIgnorer(projectDir string, scanSourcemaps bool) (*ignoreutils.Matcher, error) {
	patterns := ignoreutils.DefaultPatterns()
	if scanSourcemaps {
		patterns = append(patterns, "!*.map")
	}
	filePatterns, err := ignoreutils.ReadPatterns(projectDir)
	if err != nil {
		return nil, fmt.Errorf("secretguard: %w", err)
	}
	patterns = append(patterns, filePatterns...)
	m, err := ignoreutils.New(patterns)
	if err != nil {
		return nil, fmt.Errorf("secretguard: %w", err)
	}
	return m, nil
}

// looksBinary reports whether head — the first up-to-binarySniffBytes bytes
// of a file — looks like non-text content. This is the same NUL-byte
// heuristic `git`/`grep -I` use: real source, config, JSON, HTML, CSS and
// (even heavily minified) JS never legitimately contain a NUL byte, while
// essentially every binary format does within its first few hundred bytes.
func looksBinary(head []byte) bool {
	return bytes.IndexByte(head, 0) != -1
}

// scanFile inspects one file for secret patterns.
//
// It first reads only a small, fixed-size header (regardless of the
// file's total size) to decide whether the file looks like text at all —
// this keeps a huge binary asset (a native addon, an image, a wasm blob)
// cheap to skip no matter how large it is. Only once a file has passed
// that sniff AND fits under maxSize is the whole file read into memory and
// scanned.
//
// Unlike the previous bufio.Scanner-based implementation, lines are split
// with bytes.Split rather than through a Scanner with a fixed 64KB token
// limit: a minified/bundled JS or CSS file routinely emits one line far
// longer than that, and bufio.Scanner returning bufio.ErrTooLong for such
// a line used to discard every match already found in that file (the
// caller treated any scanFile error as "skip this file", silently passing
// exactly the minified bundle this scan most needs to catch). Reading the
// whole (size-bounded) file and splitting in memory has no such line-length
// ceiling.

// looksLikeJWTHeader reports whether token's first, dot-delimited segment is
// base64url that decodes to a JSON object containing an "alg" key — the one
// field every real JWT header carries (RFC 7519 §5.1). A bare "three
// dot-separated base64url runs starting with eyJ" shape is not unique enough
// to a JWT on its own (see the rule's comment above), so this is what turns
// that shape into a genuinely low-false-positive check: an arbitrary base64
// blob that happens to start with "eyJ" and contain two dots would have to
// ALSO decode to valid JSON with an "alg" field, which is not something
// ordinary data does by coincidence.
func looksLikeJWTHeader(token string) bool {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return false
	}
	// JWT uses unpadded base64url (RFC 4648 §5), hence RawURLEncoding.
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var header map[string]any
	if err := json.Unmarshal(decoded, &header); err != nil {
		return false
	}
	_, ok := header["alg"]
	return ok
}

// lineIsAnnotated reports whether the line at idx is exempted by an inline
// marker, either on that line or on the one immediately above it.
//
// Both positions are accepted because a long line — a redaction table, a test
// fixture — is usually annotated above rather than pushed wider, and requiring
// the marker on the flagged line would make the feature unusable exactly where
// lines are longest.
func lineIsAnnotated(lines [][]byte, idx int) bool {
	if idx < 0 || idx >= len(lines) {
		return false
	}
	if bytes.Contains(lines[idx], []byte(ports.AllowSecretMarker)) {
		return true
	}
	if idx > 0 && bytes.Contains(lines[idx-1], []byte(ports.AllowSecretMarker)) {
		return true
	}
	return false
}

func scanFile(absPath, relPath string, allowPatterns []*regexp.Regexp, maxSize int64) ([]ports.SecretMatch, *ports.SecretSkip, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	head := make([]byte, binarySniffBytes)
	n, err := f.Read(head)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	head = head[:n]
	if looksBinary(head) {
		return nil, nil, nil
	}

	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Size() > maxSize {
		return nil, &ports.SecretSkip{
			FilePath: relPath,
			Reason:   fmt.Sprintf("file is %d bytes, exceeds the %d byte text-scan limit (MaxFileSizeBytes)", info.Size(), maxSize),
		}, nil
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}

	var matches []ports.SecretMatch
	lines := bytes.Split(data, []byte("\n"))
	for i, lineBytes := range lines {
		lineNo := i + 1
		line := string(lineBytes)

		// An inline marker exempts this line without anyone having to describe
		// its content in a config file. Checked before the regexes because it is
		// the cheaper test and the more specific intent.
		if lineIsAnnotated(lines, i) {
			continue
		}

		allowed := false
		for _, allowRE := range allowPatterns {
			if allowRE.MatchString(line) {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}

		// Unlike a hand-written source line, one logical "line" in a
		// minified/bundled file can legitimately be the entire file — so,
		// unlike the previous "one match per line is enough" behavior
		// (a single FindStringIndex + break), every rule reports every
		// non-overlapping match on the line via FindAllStringIndex. A
		// single first-match-only check would silently hide every secret
		// after the first one on that line, which for a minified bundle
		// means every secret after the first ever inlined into it.
		for _, r := range defaultSecretRules {
			for _, loc := range r.Pattern.FindAllStringIndex(line, -1) {
				snippet := line[loc[0]:loc[1]]
				if r.Validate != nil && !r.Validate(snippet) {
					continue
				}
				if len(snippet) > 40 {
					snippet = snippet[:37] + "..."
				}
				matches = append(matches, ports.SecretMatch{
					FilePath:   relPath,
					LineNumber: lineNo,
					// loc[0] is a 0-based byte offset into the line; reported
					// 1-based to match how editors and `cut -c` count columns.
					Column:        loc[0] + 1,
					RuleName:      r.Name,
					SecretSnippet: snippet,
				})
			}
		}
	}
	return matches, nil, nil
}
