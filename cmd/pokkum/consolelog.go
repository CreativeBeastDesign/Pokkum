package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
)

// Human-facing console rendering for slog, written by hand rather than pulled in.
//
// A colour library (fatih/color and friends) would be a new module dependency in
// a project whose stated identity is zero-dependency, to emit escape sequences
// that are four bytes long. golang.org/x/term is already a direct dependency —
// pokkum init uses it to decide whether to prompt — so terminal detection costs
// nothing new either.
//
// The split that matters: this renderer is used only when stderr is a terminal.
// Anything piped, redirected, or captured in CI keeps the original logfmt
// output, byte for byte. Build logs are parsed by other programs, and making
// them prettier at the cost of making them unparseable would be a bad trade —
// so the pretty path is strictly additive, reachable only where a human is
// definitely reading.

// ANSI attributes. Written as constants rather than a helper indirection so the
// escape sequences are visible at the point of use.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// Inline-vs-block width budget. Measured against the real terminal width where
// one is available, because a fixed budget gets both cases wrong: too small and
// a list of similar findings explodes into five indented lines each on a wide
// terminal, too large and everything wraps unreadably on a narrow one.
const (
	fallbackInlineAttrBudget = 100
	minInlineAttrBudget      = 60
	maxInlineAttrBudget      = 160
)

// detectInlineAttrBudget reads the terminal width once, at handler construction,
// rather than per record: a build emits hundreds of records and an ioctl per line
// is wasted work for a value that essentially never changes mid-build. A resize
// during a build therefore keeps the original budget, which is a fair trade.
func detectInlineAttrBudget(f *os.File) int {
	if f == nil {
		return fallbackInlineAttrBudget
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 {
		return fallbackInlineAttrBudget
	}
	// Leave a couple of columns rather than filling to the edge, so a line that
	// just fits does not sit flush against the border.
	w -= 2
	if w < minInlineAttrBudget {
		return minInlineAttrBudget
	}
	if w > maxInlineAttrBudget {
		return maxInlineAttrBudget
	}
	return w
}

// consoleHandler renders records for a human: a level glyph, the message, and
// attributes either inline (when short) or as an aligned indented block.
//
// Timestamps are omitted deliberately. Interactively they are the least useful
// column and the widest — a full RFC3339 stamp with a timezone offset consumed
// about a third of every line in the original output, pushing the message itself
// past the point where the eye lands. Anything that needs timestamps is not a
// terminal and gets logfmt.
type consoleHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	level slog.Level
	color bool

	// attrs and groups carry what WithAttrs/WithGroup accumulated, so the
	// handler honours the slog.Handler contract rather than quietly dropping
	// pre-bound context — a logger built with With() must not lose its fields.
	attrs  []slog.Attr
	groups []string

	// inlineBudget is the screen width attributes may occupy on the message
	// line before they are broken into an indented block.
	inlineBudget int
}

// newConsoleHandler builds a handler writing to w. color should already account
// for terminal detection and NO_COLOR.
func newConsoleHandler(w io.Writer, level slog.Level, color bool) *consoleHandler {
	budget := fallbackInlineAttrBudget
	if f, ok := w.(*os.File); ok {
		budget = detectInlineAttrBudget(f)
	}
	return &consoleHandler{mu: &sync.Mutex{}, w: w, level: level, color: color, inlineBudget: budget}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := h.clone()
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := h.clone()
	next.groups = append(next.groups, name)
	return next
}

// clone copies the slices rather than sharing them, since WithAttrs may be
// called more than once on the same parent and an append could otherwise write
// into a sibling's backing array. The mutex pointer is shared on purpose: every
// derived handler writes to the same stream and must serialise against the
// others, which matters here because the build fans out over platforms and logs
// concurrently.
func (h *consoleHandler) clone() *consoleHandler {
	next := *h
	next.attrs = append([]slog.Attr(nil), h.attrs...)
	next.groups = append([]string(nil), h.groups...)
	return &next
}

func (h *consoleHandler) paint(s, attr string) string {
	if !h.color || s == "" {
		return s
	}
	return attr + s + ansiReset
}

// levelGlyph returns the marker and its colour for a level. Glyphs carry the
// severity at a glance and keep working when colour is off — which is the point
// of using them rather than relying on colour alone, since NO_COLOR, a dumb
// terminal and colour-blind readers all have to end up with a legible line.
func levelGlyph(l slog.Level) (string, string) {
	switch {
	case l >= slog.LevelError:
		return "✗", ansiRed
	case l >= slog.LevelWarn:
		return "⚠", ansiYellow
	case l >= slog.LevelInfo:
		return "•", ansiCyan
	default:
		return "·", ansiDim
	}
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	glyph, glyphColor := levelGlyph(r.Level)

	// Collect attributes in order: those bound via With() first, then the
	// record's own, so a logger's context reads before the specifics.
	type kv struct{ k, v string }
	var pairs []kv
	add := func(a slog.Attr) {
		if a.Equal(slog.Attr{}) {
			return
		}
		key := a.Key
		if len(h.groups) > 0 {
			key = strings.Join(h.groups, ".") + "." + key
		}
		pairs = append(pairs, kv{k: key, v: fmt.Sprint(a.Value.Resolve().Any())})
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		add(a)
		return true
	})

	msg := r.Message
	if h.color {
		msg = h.paint(msg, ansiBold)
	}

	var b strings.Builder
	b.WriteString(h.paint(glyph, glyphColor))
	b.WriteByte(' ')
	b.WriteString(msg)

	// Decide inline vs block from the *unpainted* width: escape sequences are
	// zero-width on screen, so measuring the coloured string would misjudge the
	// budget and wrap lines that actually fit.
	plainWidth := 2 + utf8.RuneCountInString(r.Message)
	var inline strings.Builder
	for _, p := range pairs {
		inline.WriteByte(' ')
		inline.WriteString(p.k)
		inline.WriteByte('=')
		inline.WriteString(p.v)
	}

	switch {
	case len(pairs) == 0:
		// nothing to append
	case plainWidth+utf8.RuneCountInString(inline.String()) <= h.inlineBudget && !strings.ContainsAny(inline.String(), "\n"):
		b.WriteString(h.paint(inline.String(), ansiDim))
	default:
		// Block form, keys aligned so values line up into a column the eye can
		// scan down. This is the case the original single-line output handled
		// worst: six attributes on one 200-column line.
		width := 0
		for _, p := range pairs {
			if len(p.k) > width {
				width = len(p.k)
			}
		}
		for _, p := range pairs {
			b.WriteByte('\n')
			b.WriteString("    ")
			b.WriteString(h.paint(fmt.Sprintf("%-*s", width, p.k), ansiDim))
			b.WriteString("  ")
			// Multi-line values (a wrapped error, a captured stderr) are
			// indented to the value column so the continuation stays visually
			// inside its own field rather than looking like a new record.
			b.WriteString(strings.ReplaceAll(p.v, "\n", "\n    "+strings.Repeat(" ", width+2)))
		}
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// consoleRenderingWanted reports whether stderr should get the human renderer,
// and whether colour is appropriate.
//
// Both answers are conservative: anything that is not clearly an interactive
// terminal keeps logfmt, and colour additionally honours NO_COLOR
// (https://no-color.org) and TERM=dumb. A build log that ends up in a file with
// escape sequences in it is worse than one that was never coloured.
func consoleRenderingWanted(f *os.File) (console bool, color bool) {
	if f == nil {
		return false, false
	}
	if !term.IsTerminal(int(f.Fd())) {
		return false, false
	}
	if os.Getenv("TERM") == "dumb" {
		// A terminal that cannot render attributes still benefits from the
		// glyphs and the indented block, so keep the layout and drop colour.
		return true, false
	}
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor {
		return true, false
	}
	return true, true
}
