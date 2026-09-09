package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// ---------------------------------------------------------------------------
// Anti-drift guards for `pokkum guide`.
//
// The guide deliberately duplicates reference material that also lives in
// Vocabulary.md, because its audience — someone working in a SvelteKit project
// rather than in this repository — cannot read Vocabulary.md at all. That
// duplication is only defensible if it cannot rot, which is what these guards
// are for. They follow the same discipline as flags_docs_test.go: the prose is
// authored, and a test checks it against the real code surface.
//
// A guide that silently omits a config field is worse than no guide, because a
// reader has no way to tell the difference between "not documented" and "does
// not exist" — and will conclude the latter.
// ---------------------------------------------------------------------------

// guideText renders every section, as `pokkum guide` with no argument does.
func guideText() string { return renderGuide(guideSections) }

// configSectionText returns just the config reference section, which is where
// every .pokkum.yaml field is required to appear. Scoping the assertion to
// that one section is deliberate: a field name that happens to appear in an
// unrelated sentence elsewhere must not satisfy the requirement.
func configSectionText(t *testing.T) string {
	t.Helper()
	for _, s := range guideSections {
		if s.Topic == "config" {
			return s.Body
		}
	}
	t.Fatal("[TEST SETUP] no section with topic \"config\"; the guard cannot run")
	return ""
}

// yamlFieldNames walks a struct type recursively and returns every name
// declared in a `yaml:` tag, skipping "-" and inline/omitempty modifiers.
// Reflection is used rather than an AST scan so the guard reads the same type
// the parser actually binds to.
func yamlFieldNames(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
		yamlFieldNames(f.Type, seen, out)
	}
}

// TestGuideDocumentsEveryConfigField is the load-bearing guard: a new
// .pokkum.yaml field cannot ship with the guide silently incomplete.
func TestGuideDocumentsEveryConfigField(t *testing.T) {
	fields := map[string]bool{}
	seen := map[reflect.Type]bool{}
	yamlFieldNames(reflect.TypeOf(ports.ProjectConfig{}), seen, fields)
	yamlFieldNames(reflect.TypeOf(ports.BuildProfile{}), seen, fields)

	// Premise check: a scan that finds nothing would make this test pass while
	// verifying nothing at all.
	if len(fields) < 30 {
		t.Fatalf("[TEST SETUP] reflection found only %d yaml fields across ports.ProjectConfig and "+
			"ports.BuildProfile; the walk has gone blind", len(fields))
	}
	for _, must := range []string{"repo", "require_env", "fail_on_cve", "update_image", "profiles"} {
		if !fields[must] {
			t.Fatalf("[TEST SETUP] reflection did not find known field %q; the walk is wrong", must)
		}
	}

	section := configSectionText(t)
	var missing []string
	for name := range fields {
		if !strings.Contains(section, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("pokkum guide's config section does not mention %d .pokkum.yaml field(s): %v\n"+
			"\tEvery field of ports.ProjectConfig/ports.BuildProfile must appear in the \"config\" section\n"+
			"\tof cmd/pokkum/guide.go. A reader cannot distinguish an undocumented field from a\n"+
			"\tnonexistent one, and will assume it does not exist.", len(missing), missing)
	}
	t.Logf("checked %d yaml fields against the guide's config section", len(fields))
}

// TestGuideMentionsEveryCommand stops a new subcommand from shipping without
// the guide knowing it exists.
func TestGuideMentionsEveryCommand(t *testing.T) {
	root := newRootCommandForTest(t)
	text := guideText()

	var checked int
	var missing []string
	for _, c := range root.Commands() {
		name := c.Name()
		// hermetic-reexec is an internal re-entry point invoked by the build
		// itself, never by a user; documenting it would invite someone to run it.
		if strings.HasPrefix(name, "__") || name == "help" || name == "completion" {
			continue
		}
		checked++
		if !strings.Contains(text, "pokkum "+name) {
			missing = append(missing, name)
		}
	}
	if checked == 0 {
		t.Fatal("[TEST SETUP] walked zero commands; the command tree is not being built")
	}
	if len(missing) > 0 {
		t.Errorf("pokkum guide never mentions %d command(s): %v\n"+
			"\tAdd each to the \"overview\" section's command list in cmd/pokkum/guide.go.", len(missing), missing)
	}
	t.Logf("checked %d commands against the guide", checked)
}

// TestGuideTopicsAreAddressable asserts the topic index and the section set
// cannot diverge: every advertised topic resolves, and an unknown one is a
// usage error naming the valid set rather than a silent empty print.
func TestGuideTopicsAreAddressable(t *testing.T) {
	topics := guideTopicNames()
	if len(topics) == 0 {
		t.Fatal("[TEST SETUP] no guide topics defined")
	}

	for _, topic := range topics {
		var buf bytes.Buffer
		cmd := newGuideCommand(discardLogger())
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{topic})
		if err := cmd.Execute(); err != nil {
			t.Errorf("pokkum guide %s: unexpected error: %v", topic, err)
			continue
		}
		if strings.TrimSpace(buf.String()) == "" {
			t.Errorf("pokkum guide %s printed nothing", topic)
		}
	}

	// The index must list exactly the sections that exist.
	var idx bytes.Buffer
	listCmd := newGuideCommand(discardLogger())
	listCmd.SetOut(&idx)
	listCmd.SetArgs([]string{"topics"})
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("pokkum guide topics: %v", err)
	}
	for _, topic := range topics {
		if !strings.Contains(idx.String(), topic) {
			t.Errorf("pokkum guide topics omits %q", topic)
		}
	}

	// An unknown topic must fail loudly and name the alternatives.
	var errBuf bytes.Buffer
	bad := newGuideCommand(discardLogger())
	bad.SetOut(&errBuf)
	bad.SetErr(&errBuf)
	bad.SetArgs([]string{"no-such-topic"})
	err := bad.Execute()
	if err == nil {
		t.Error("pokkum guide no-such-topic returned nil error; an unknown topic must be a usage error")
	} else if !strings.Contains(err.Error(), topics[0]) {
		t.Errorf("unknown-topic error does not name the valid topics: %v", err)
	}
}

// TestGuideStatesTheReadOnlyInvariant pins the specific facts that were
// discovered the expensive way — as a production failure — rather than from
// any document. They are the reason the guide exists, so they are asserted
// individually rather than left to the prose.
func TestGuideStatesTheReadOnlyInvariant(t *testing.T) {
	text := guideText()
	for _, want := range []struct{ needle, why string }{
		{"read-only", "the application tree ships at mode 0555 with no opt-out"},
		{"0555", "the concrete mode, so a reader can match it against what they observe"},
		{"DAC_OVERRIDE", "why a docker exec check as root proves nothing"},
		{"temp", "where runtime writes must go instead"},
	} {
		if !strings.Contains(text, want.needle) {
			t.Errorf("pokkum guide never says %q — %s", want.needle, want.why)
		}
	}
}

// newRootCommandForTest builds the real command tree the binary registers.
func newRootCommandForTest(t *testing.T) *cobra.Command {
	t.Helper()
	root := newRootCommand(context.Background(), discardLogger())
	if root == nil {
		t.Fatal("[TEST SETUP] newRootCommand returned nil")
	}
	return root
}

// TestGuideNamesEveryStaticVerdict couples the guide's strategy section to the
// classifier's actual verdict vocabulary.
//
// It is narrow on purpose. The blocker RULES are matchers inside
// sveltekitutils, not an enumerable set, so nothing here can mechanically
// prove the guide's rule table still matches them — which is why the section
// tells the reader pokkum init is authoritative. The verdicts ARE enumerable,
// so at minimum a renamed or added verdict cannot ship with the guide still
// describing the old three. See the roadmap item static-rules-guide-coupling.
func TestGuideNamesEveryStaticVerdict(t *testing.T) {
	var section string
	for _, s := range guideSections {
		if s.Topic == "strategy" {
			section = s.Body
		}
	}
	if section == "" {
		t.Fatal("[TEST SETUP] no section with topic \"strategy\"")
	}
	for _, v := range []ports.StaticVerdict{ports.StaticViable, ports.StaticBlocked, ports.StaticUnknown} {
		if !strings.Contains(section, string(v)) {
			t.Errorf("pokkum guide's strategy section never uses the verdict %q that pokkum init reports", v)
		}
	}
}
