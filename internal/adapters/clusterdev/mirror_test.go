package clusterdev

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The two values below MUST be byte-identical in two separately compiled
// programs: the pokkum CLI (which builds the `kubectl exec` argv and reads the
// pod's env) and pokkum-init (which gates on the env and dispatches the
// subcommand). pokkum-init cannot import internal/ports — it ships as a
// go:embed blob inside the CLI, and importing ports would drag
// go-containerregistry into a program whose job is to fork one child and count
// signals — so the constants are hand-copied, and a doc comment is the only
// other thing holding them together.
//
// A doc comment is not a control. It fails silently, at runtime, only in the
// shipped artifact, and only after every check has gone green. This file is
// the control: it parses pokkum-init's REAL declarations out of its source and
// compares them, in the same shape as
// attestutils.TestAttestationRoots_MatchSupervisorMirror — the guard that
// exists because adding /app/node_modules to one side and not the other
// bricked every layered image while `go test ./...` reported all green.
//
// What drift would actually cost here. A changed env name means the CLI reads
// POKKUM_DEV_MODE off the pod spec, reports the target as dev-enabled and
// proceeds through a full SvelteKit build, while pokkum-init gates on a name
// nothing sets and refuses the sync at the far end. A changed subcommand name
// means `kubectl exec ... -- /pokkum/init __dev-sync` is parsed as an ordinary
// supervisor invocation carrying an unknown flag, and exits 2.
const (
	supervisorConfigSource  = "config.go"
	supervisorDevSyncSource = "devsync.go"
)

func supervisorSource(file string) string {
	return filepath.Join("..", "..", "..", "supervisor", "cmd", "pokkum-init", file)
}

// parseSupervisorStringConst reads the single string-literal declaration named
// `name` out of `path`.
//
// It returns an error — rather than taking a *testing.T and calling Fatalf —
// specifically so TestSupervisorMirrorCheckCanFail below can prove that a
// missing or non-literal declaration is refused. A parser that silently
// returned the empty string would make every comparison in this file vacuous:
// two empty strings compare equal, and the guard would report parity forever.
// That is the second trap row 51 of the self-review checklist names.
func parseSupervisorStringConst(path, name string) (string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}

	var got string
	var found bool
	var inspectErr error
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != name || len(vs.Values) != 1 {
			return true
		}
		bl, ok := vs.Values[0].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			inspectErr = fmt.Errorf("%s in %s is not a plain string literal (%T); keep it one so this drift check can read it", name, path, vs.Values[0])
			return false
		}
		v, uerr := strconv.Unquote(bl.Value)
		if uerr != nil {
			inspectErr = fmt.Errorf("unquote %s: %w", bl.Value, uerr)
			return false
		}
		got, found = v, true
		return false
	})

	switch {
	case inspectErr != nil:
		return "", inspectErr
	case !found:
		return "", fmt.Errorf("could not find the %s declaration in %s — if it was renamed or restructured, update this test rather than deleting it", name, path)
	}
	return got, nil
}

func mustParseSupervisorConst(t *testing.T, file, name string) string {
	t.Helper()
	v, err := parseSupervisorStringConst(supervisorSource(file), name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return v
}

// TestDevModeEnvName_MatchesSupervisorMirror pins ports.EnvDevMode against
// pokkum-init's own envDevMode.
func TestDevModeEnvName_MatchesSupervisorMirror(t *testing.T) {
	got := mustParseSupervisorConst(t, supervisorConfigSource, "envDevMode")
	if got != ports.EnvDevMode {
		t.Fatalf("dev-mode env name has drifted:\n  pokkum-init: %q\n  ports:       %q\n"+
			"The CLI would report a container as dev-enabled and build the whole project, while pokkum-init gates on a variable nothing sets.",
			got, ports.EnvDevMode)
	}
}

// TestDevSyncSubcommand_MatchesSupervisorMirror pins ports.DevSyncSubcommand
// against pokkum-init's own devSyncSubcommand — the argv the adapter execs
// into the pod.
func TestDevSyncSubcommand_MatchesSupervisorMirror(t *testing.T) {
	got := mustParseSupervisorConst(t, supervisorDevSyncSource, "devSyncSubcommand")
	if got != ports.DevSyncSubcommand {
		t.Fatalf("dev-sync subcommand name has drifted:\n  pokkum-init: %q\n  ports:       %q\n"+
			"`kubectl exec ... -- %s %s` would be parsed as an ordinary supervisor invocation with an unknown flag.",
			got, ports.DevSyncSubcommand, ports.SupervisorPath, ports.DevSyncSubcommand)
	}
}

// TestSupervisorMirrorCheckCanFail guards the guard: it proves the parser
// refuses rather than returning a quiet empty string, which is the only way
// the two comparisons above can be trusted to mean anything.
func TestSupervisorMirrorCheckCanFail(t *testing.T) {
	t.Run("missing declaration", func(t *testing.T) {
		got, err := parseSupervisorStringConst(supervisorSource(supervisorConfigSource), "thisConstantDoesNotExist")
		if err == nil {
			t.Fatalf("parsing a name that is not in the source returned %q and no error; every comparison built on it would be vacuous", got)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if _, err := parseSupervisorStringConst(supervisorSource("no_such_file.go"), "envDevMode"); err == nil {
			t.Fatal("parsing a file that does not exist returned no error; a moved supervisor source would silently disable this guard")
		}
	})

	t.Run("a real declaration is found and is not empty", func(t *testing.T) {
		// Without this, both negative cases above would also pass against a
		// parser that returns an error unconditionally.
		got, err := parseSupervisorStringConst(supervisorSource(supervisorConfigSource), "envDevMode")
		if err != nil {
			t.Fatalf("parsing the real declaration failed: %v", err)
		}
		if got == "" {
			t.Fatal("the real declaration parsed as the empty string")
		}
	})
}
