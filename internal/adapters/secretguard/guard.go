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

	// RequiredAny lists byte sequences of which AT LEAST ONE must occur in any
	// text Pattern is capable of matching. This is a necessary condition read
	// straight off the regex — not a heuristic, not a sampling trick: every
	// alternative Pattern can take contains one of these literals, so
	// `!containsAny(data, RequiredAny)` is a *proof* that Pattern matches
	// nowhere in data, and the rule can be dropped for that whole file. The
	// prefilter can therefore only ever remove work, never a detection.
	//
	// Comparison is byte-exact, which is sound only because no rule carrying a
	// RequiredAny is case-insensitive; a `(?i)` rule uses RequiredAnyFold
	// instead. TestPrefilter_CaseSensitivityMatchesLiteralKind machine-checks
	// that pairing against each Pattern's parsed flags rather than trusting
	// this comment.
	//
	// An empty RequiredAny (and empty RequiredAnyFold) means "this rule has no
	// unambiguous mandatory literal": it then stays active for every file,
	// which is exactly the pre-prefilter behaviour.
	RequiredAny [][]byte

	// RequiredAnyFold is RequiredAny for a case-insensitive Pattern. Entries
	// are lowercase ASCII and are compared after ASCII case folding, with the
	// non-ASCII runes that Unicode simple case folding maps onto those ASCII
	// letters handled separately via foldEscapeSequences — see activeRules.
	RequiredAnyFold [][]byte
}

var defaultSecretRules = []rule{
	{
		Name:    "RSA Private Key",
		Pattern: regexp.MustCompile(`-----BEGIN (?:RSA )?PRIVATE KEY-----`),
		// The pattern opens with this literal and nothing optional precedes
		// it, so every string it matches starts with these 11 bytes.
		RequiredAny: [][]byte{[]byte("-----BEGIN ")},
	},
	{
		Name:        "AWS Access Key ID",
		Pattern:     regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		RequiredAny: [][]byte{[]byte("AKIA")},
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
		Pattern:     regexp.MustCompile(`\bghp_[a-zA-Z0-9]{36,255}\b`),
		RequiredAny: [][]byte{[]byte("ghp_")},
	},
	{
		Name: "GitHub App Token",
		// gho_ (OAuth), ghu_ (user-to-server), ghs_ (server-to-server) share
		// the same value shape as ghp_ but are not personal access tokens,
		// so they get their own rule name — a finding should say which kind
		// of GitHub credential was found, not lump every gh*_ prefix under
		// "personal access token".
		Pattern: regexp.MustCompile(`\bgh[osu]_[a-zA-Z0-9]{36,255}\b`),
		// A character class, not a literal: the mandatory prefix is one of
		// three alternatives, so all three are listed and the prefilter keeps
		// the rule if ANY of them appears. Listing only `gh` would also be
		// correct but far less selective; listing only `gho_` would NOT be —
		// it would silently drop ghs_/ghu_ tokens.
		RequiredAny: [][]byte{[]byte("gho_"), []byte("ghs_"), []byte("ghu_")},
	},
	{
		Name: "Slack Token",
		// xoxb- (bot) / xoxp- (user) tokens: <workspace-id>-<id>-<secret>,
		// with the user token format sometimes carrying a fourth numeric
		// segment. The xoxb-/xoxp- literal prefix is essentially never seen
		// outside a real Slack token, so this stays high-signal even with a
		// fairly permissive tail.
		Pattern:     regexp.MustCompile(`\bxox[bp]-[0-9]+-[0-9]+-(?:[0-9]+-)?[a-zA-Z0-9]+\b`),
		RequiredAny: [][]byte{[]byte("xoxb-"), []byte("xoxp-")},
	},
	{
		Name:        "Stripe Live Secret Key",
		Pattern:     regexp.MustCompile(`\bsk_live_[a-zA-Z0-9]{16,99}\b`),
		RequiredAny: [][]byte{[]byte("sk_live_")},
	},
	{
		Name:        "GitLab Personal Access Token",
		Pattern:     regexp.MustCompile(`\bglpat-[a-zA-Z0-9_-]{20,50}\b`),
		RequiredAny: [][]byte{[]byte("glpat-")},
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
		Pattern:     regexp.MustCompile(`\beyJ[a-zA-Z0-9_-]{5,}\.[a-zA-Z0-9_-]{10,}\.[a-zA-Z0-9_-]{10,}`),
		Validate:    looksLikeJWTHeader,
		RequiredAny: [][]byte{[]byte("eyJ")},
	},
	{
		Name:        "Google API Key",
		Pattern:     regexp.MustCompile(`\bAIzaSy[a-zA-Z0-9_-]{33}\b`),
		RequiredAny: [][]byte{[]byte("AIzaSy")},
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
		//
		// The false-positive cost of that widening was MEASURED against real
		// bundles rather than assumed — see minified_corpus_test.go, which
		// makes the measurement a permanent test. Both key patterns were also
		// swept over every real .js/.mjs/.cjs/.ts under testdata/ (4598 files,
		// 105.6MiB, including the published npm bundles of the Svelte
		// compiler, Vite, TypeScript, Rollup, Rolldown and esbuild): the old
		// word-anchored key found 0, the widened one found 24. All 24 are one
		// class — Vite's bundled js-tokens lexer assigning a local named
		// `lastSignificantToken` to sentinel strings like
		// "?InterpolationInTemplate" — and all 24 sit under node_modules/,
		// which ScanDirectory never walks. Over the file set the scanner
		// actually reaches in this repo's three real SvelteKit fixtures, the
		// count is 0 before and 0 after.
		//
		// The residual risk is therefore a non-credential identifier that
		// genuinely ENDS in Token/Secret/Password/ApiKey and lands somewhere
		// the scanner does walk. Nothing structural separates
		// `lastSignificantToken` from `accessToken`, so no tightening on the
		// key side can exclude one and keep the other; naming such identifiers
		// in a stop-word list was rejected because an allowlist of exempted key
		// names decays into a pre-authorised blind spot for a key genuinely
		// named that way. The escape hatches for that case are the existing
		// per-line `pokkum:allow-secret` marker and AllowPatterns.
		Pattern: regexp.MustCompile(`(?i)\b[a-z0-9_]*(?:password|secret|token|api_?key)\s*[:=]\s*["']([^"'\s(){}\[\];]{8,})["']`),
		// The one case-insensitive rule, and the most expensive one to run:
		// case-folded with no literal prefix for the regex engine to skip
		// ahead on, so without a gate it re-reads every line of every file.
		// Its key alternation is mandatory — a match must contain one of these
		// four spellings (five, since `api_?key` covers both `apikey` and
		// `api_key`) — so a case-insensitive search for them over the whole
		// file is a necessary condition, exactly like RequiredAny above.
		RequiredAnyFold: [][]byte{
			[]byte("password"),
			[]byte("secret"),
			[]byte("token"),
			[]byte("apikey"),
			[]byte("api_key"),
		},
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

// foldEscapeSequences are the UTF-8 encodings of every NON-ASCII rune whose
// Unicode simple-case-fold orbit contains an ASCII letter used in any
// RequiredAnyFold literal. Go's regexp implements `(?i)` with Unicode simple
// case folding, not ASCII case folding, so `(?i)secret` really does match
// "\u017fecret" and `(?i)token` really does match "to\u212Aen" — an
// ASCII-only lowercase fold would declare the generic rule inactive for a file
// containing exactly those, and lose a detection the old code made.
//
// Rather than fold these runes (which changes byte length and complicates the
// windowed scan), their mere presence anywhere in the file keeps every folded
// rule active. That is strictly conservative: it can only add work.
//
// The list is derived, not guessed — TestPrefilter_FoldEscapesAreComplete
// re-derives it by brute-forcing unicode.SimpleFold over the whole code point
// space and fails if Go's tables ever grow another one.
var foldEscapeSequences = [][]byte{
	[]byte("\u017f"), // LATIN SMALL LETTER LONG S — folds onto 's'/'S'
	[]byte("\u212a"), // KELVIN SIGN — folds onto 'k'/'K'
}

// foldWindowBytes is the working-window size for containsAnyFold. Folding a
// window at a time (rather than the whole file) keeps the scratch buffer at a
// fixed 32KiB regardless of file size — a 16MiB bundle does not cost a 16MiB
// shadow copy — and lets the search stop at the first hit instead of folding
// bytes nobody will look at.
const foldWindowBytes = 32 << 10

// allowSecretMarkerBytes is the inline allow marker as bytes. Hoisted to a
// package-level var because lineIsAnnotated is called once per line of every
// scanned file, and `[]byte(constString)` inside that loop is a fresh
// allocation and copy per call.
var allowSecretMarkerBytes = []byte(ports.AllowSecretMarker)

// containsAny reports whether data contains at least one of lits, compared
// byte-exactly.
func containsAny(data []byte, lits [][]byte) bool {
	for _, lit := range lits {
		if bytes.Contains(data, lit) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether data contains at least one of lowered —
// which must be all-lowercase ASCII — under ASCII case folding.
//
// data is walked in fixed windows that overlap by len(longest lowered)-1
// bytes, so a literal straddling a window boundary is still seen. scratch is
// a caller-owned reusable buffer; it is grown once and then reused for every
// file in a scan.
func containsAnyFold(data []byte, lowered [][]byte, scratch *[]byte) bool {
	longest := 0
	for _, lit := range lowered {
		if len(lit) > longest {
			longest = len(lit)
		}
	}
	if longest == 0 {
		return false
	}
	overlap := longest - 1

	if cap(*scratch) < foldWindowBytes+overlap {
		*scratch = make([]byte, foldWindowBytes+overlap)
	}
	buf := (*scratch)[:cap(*scratch)]

	for start := 0; start < len(data); start += foldWindowBytes {
		lo := start - overlap
		if lo < 0 {
			lo = 0
		}
		hi := start + foldWindowBytes
		if hi > len(data) {
			hi = len(data)
		}
		win := buf[:hi-lo]
		for i, c := range data[lo:hi] {
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			win[i] = c
		}
		for _, lit := range lowered {
			if bytes.Contains(win, lit) {
				return true
			}
		}
	}
	return false
}

// scanScratch holds the buffers reused across the files of a single
// ScanDirectory walk. It is deliberately per-call state, never package state:
// two concurrent ScanDirectory calls each get their own, so there is nothing
// to race on. The walk callback itself is sequential, so no lock is needed.
type scanScratch struct {
	fold  []byte
	rules []int
}

// activeRules returns the indices into defaultSecretRules of the rules that
// can possibly match somewhere in data.
//
// Every exclusion here is backed by a proof, never by sampling: a rule is
// dropped only when a literal that EVERY string its pattern can match must
// contain is absent from the entire file. A literal cannot appear on a line
// without appearing in the file, so "absent from the file" implies "absent
// from every line", and the per-line loop below could not have matched it.
//
// The returned slice aliases the scratch buffer and is only valid until the
// next call.
func (s *scanScratch) activeRules(data []byte) []int {
	out := s.rules[:0]
	// -1 = not yet determined; the check is shared by every folded rule.
	foldEscaped := -1
	for i := range defaultSecretRules {
		r := &defaultSecretRules[i]
		switch {
		case len(r.RequiredAny) > 0:
			if containsAny(data, r.RequiredAny) {
				out = append(out, i)
			}
		case len(r.RequiredAnyFold) > 0:
			if foldEscaped < 0 {
				foldEscaped = 0
				if containsAny(data, foldEscapeSequences) {
					foldEscaped = 1
				}
			}
			if foldEscaped == 1 || containsAnyFold(data, r.RequiredAnyFold, &s.fold) {
				out = append(out, i)
			}
		default:
			// No mandatory literal could be derived from this pattern: it has
			// to be run against every line, exactly as before.
			out = append(out, i)
		}
	}
	s.rules = out
	return out
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

	// One set of reusable buffers for the whole walk. WalkDir's callback runs
	// sequentially on this goroutine, and the scratch is local to this call,
	// so it is neither shared across goroutines nor across concurrent scans.
	scratch := &scanScratch{}

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

		// The walk already holds this entry; taking the size from it saves an
		// fstat per file. Only for a regular file, though: fs.DirEntry.Info is
		// lstat-shaped, so for a symlink it reports the length of the target
		// path rather than the size of the file scanFile will actually open.
		// -1 means "walk could not supply one, stat it yourself".
		//
		// The value can also be stale if the file changed between the walk's
		// readdir and the open below. Both directions stay safe: too small and
		// readRemainder still reads to EOF and scans everything; too large and
		// the file is recorded as a ports.SecretSkip, which fails the build
		// loudly rather than reporting a clean file nobody read.
		entrySize := int64(-1)
		if d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				entrySize = info.Size()
			}
		}

		fileMatches, skip, err := scanFile(path, rel, compiledAllow, maxSize, entrySize, scratch)
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
	if bytes.Contains(lines[idx], allowSecretMarkerBytes) {
		return true
	}
	if idx > 0 && bytes.Contains(lines[idx-1], allowSecretMarkerBytes) {
		return true
	}
	return false
}

func scanFile(absPath, relPath string, allowPatterns []*regexp.Regexp, maxSize, entrySize int64, scratch *scanScratch) ([]ports.SecretMatch, *ports.SecretSkip, error) {
	if scratch == nil {
		scratch = &scanScratch{}
	}

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
	// ORDER IS LOAD-BEARING: the binary sniff must stay ahead of the size
	// check. A large binary asset is meant to be skipped silently (it was
	// never scannable text); if the size check ran first it would instead be
	// recorded as a ports.SecretSkip, which forces Passed=false and fails the
	// build. See ScanDirectory's doc comment.
	if looksBinary(head) {
		return nil, nil, nil
	}

	// entrySize is the size the directory walk already learned from the
	// fs.DirEntry, saving an fstat here. It is -1 when the walk could not
	// supply a trustworthy one (a non-regular file — notably a symlink, whose
	// DirEntry reports the length of the link target's PATH, not the size of
	// the file it points at), in which case fall back to stat-ing the opened
	// descriptor exactly as before.
	if entrySize < 0 {
		info, err := f.Stat()
		if err != nil {
			return nil, nil, err
		}
		entrySize = info.Size()
	}
	if entrySize > maxSize {
		return nil, &ports.SecretSkip{
			FilePath: relPath,
			Reason:   fmt.Sprintf("file is %d bytes, exceeds the %d byte text-scan limit (MaxFileSizeBytes)", entrySize, maxSize),
		}, nil
	}

	data, err := readRemainder(f, head, entrySize)
	if err != nil {
		return nil, nil, err
	}

	return scanBytes(data, relPath, allowPatterns, scratch), nil, nil
}

// readRemainder returns head followed by everything left in f, in a buffer
// sized up front from sizeHint.
//
// This replaces the previous Seek-to-0 + io.ReadAll pair. Seeking back made
// the binary-sniff bytes get read from the kernel twice, and io.ReadAll starts
// from a 512-byte buffer and grows by repeated reallocation — for a 4MiB
// bundle that is roughly a dozen reallocations and ~8MiB of copying per file.
//
// sizeHint is only a hint: the loop keeps reading until EOF and grows if the
// file turned out longer than the directory entry claimed, so a file that
// changed size between the walk and the open is still read in full rather
// than truncated. Truncating there would be a silent false-clean, which is
// the one failure mode this adapter exists to not have.
func readRemainder(f *os.File, head []byte, sizeHint int64) ([]byte, error) {
	capacity := sizeHint
	if capacity < int64(len(head)) {
		capacity = int64(len(head))
	}
	// +1 so the final zero-length read that reports io.EOF does not force a
	// reallocation of the whole buffer.
	buf := make([]byte, len(head), capacity+1)
	copy(buf, head)
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err != nil {
			if err == io.EOF {
				return buf, nil
			}
			return nil, err
		}
	}
}

// scanBytes runs the rule set over one already-read file body.
//
// Two things happen before the per-line loop, and neither can suppress a
// finding:
//
//   - activeRules drops any rule whose mandatory literal is absent from the
//     whole file. See its doc comment for why that is a proof rather than a
//     heuristic.
//   - When the active set is empty, no rule can match any line, so there is
//     nothing for the loop to find. This is a short-circuit of work, not of
//     coverage: the file has still been read in full, and "clean" here is a
//     statement about content the scanner actually holds in memory.
//
// Lines are matched as []byte throughout. The previous code did
// `string(lineBytes)` per line, which copied the entire file a second time
// (16MiB of extra allocation for the largest file the ceiling allows) purely
// to reach regexp's string API; regexp's []byte API is the same matcher over
// the same bytes, so the conversion bought nothing. Only the bytes of an
// actual match are converted now.
func scanBytes(data []byte, relPath string, allowPatterns []*regexp.Regexp, scratch *scanScratch) []ports.SecretMatch {
	active := scratch.activeRules(data)
	if len(active) == 0 {
		return nil
	}

	var matches []ports.SecretMatch
	lines := bytes.Split(data, []byte("\n"))
	for i, lineBytes := range lines {
		lineNo := i + 1

		// An inline marker exempts this line without anyone having to describe
		// its content in a config file. Checked before the regexes because it is
		// the cheaper test and the more specific intent.
		if lineIsAnnotated(lines, i) {
			continue
		}

		allowed := false
		for _, allowRE := range allowPatterns {
			if allowRE.Match(lineBytes) {
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
		// non-overlapping match on the line via FindAllIndex. A single
		// first-match-only check would silently hide every secret after
		// the first one on that line, which for a minified bundle means
		// every secret after the first ever inlined into it.
		for _, ri := range active {
			r := &defaultSecretRules[ri]
			for _, loc := range r.Pattern.FindAllIndex(lineBytes, -1) {
				snippet := string(lineBytes[loc[0]:loc[1]])
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
	return matches
}
