package bunexec

import (
	"strings"
	"testing"
)

// The client-asset half of the handler patch.
//
// This is the bug an adversarial field test found after every structural check
// passed: the image booted, both probes returned 200, `/` answered 200, the
// assets were present in the image at /app/client — and every stylesheet and
// script 404'd. adapter-node's handler.js serves them from
// `serve(path.join(dir, 'client'), true)` where dir is the handler's own
// directory (/app/server), while Pokkum mounts the tree at /app/client.
//
// The failure is silent by construction: serve() returns false for a
// non-existent path and the middleware chain is assembled with .filter(Boolean),
// so the asset handler is dropped with no error and no log line. Nothing in
// digest inspection, layer counts, annotations, SBOM, signatures or health
// probes can see it. Only requesting an asset can.

func TestApplyClientPatch_RewritesRealAdapterNodeShape(t *testing.T) {
	// The exact expression adapter-node emits (handler.js:242).
	src := `const assets_handler = ([serve(path.join(dir, 'client'), true), serve_prerendered(), ssr].filter(Boolean));`

	patched, ok := applyClientPatch(src)
	if !ok {
		t.Fatal("client join not recognised in the real adapter-node shape")
	}
	if !strings.Contains(patched, `process.env.POKKUM_CLIENT_DIR || path.join(dir, 'client')`) {
		t.Errorf("patched source does not consult POKKUM_CLIENT_DIR:\n%s", patched)
	}
	// The second argument (`true`, the client flag) must survive: it is what
	// makes adapter-node set immutable cache headers on hashed assets.
	if !strings.Contains(patched, `), true)`) {
		t.Errorf("the client flag argument was lost, which would drop immutable caching:\n%s", patched)
	}
}

func TestApplyClientPatch_CoversQuotingAndDirVariants(t *testing.T) {
	for _, src := range []string{
		`serve(path.join(dir, "client"))`,
		`serve(path.join(dir, 'client'))`,
		`serve(path.join(__dirname, 'client'))`,
		`serve(path.join(server_dir, "client"))`,
		`serve(path.join(serverDir, 'client'))`,
	} {
		if _, ok := applyClientPatch(src); !ok {
			t.Errorf("client join not recognised: %s", src)
		}
	}
}

// TestApplyClientPatch_LeavesUnrelatedJoinsAlone guards the other direction: an
// over-broad rewrite would redirect paths that are correct as they stand.
func TestApplyClientPatch_LeavesUnrelatedJoinsAlone(t *testing.T) {
	for _, src := range []string{
		`path.join(dir, 'server')`,
		`path.join(dir, 'clientele')`,
		`path.join(other, 'client', 'nested')`,
	} {
		if got, ok := applyClientPatch(src); ok {
			t.Errorf("unrelated join was rewritten: %s -> %s", src, got)
		}
	}
}

// TestApplyHandlerPathPatches_PatchesBothIndependently proves the combined
// entry point does not require both patterns to be present. An app with client
// assets but no prerendered pages is ordinary, and before this the prerendered
// pattern alone decided whether anything was written at all.
func TestApplyHandlerPathPatches_PatchesBothIndependently(t *testing.T) {
	clientOnly := `serve(path.join(dir, 'client'), true)`
	if patched, ok := applyHandlerPathPatches(clientOnly); !ok || !strings.Contains(patched, "POKKUM_CLIENT_DIR") {
		t.Errorf("client-only handler was not patched: ok=%v src=%s", ok, patched)
	}

	prerenderedOnly := `serve(path.join(dir, 'prerendered'))`
	if patched, ok := applyHandlerPathPatches(prerenderedOnly); !ok || !strings.Contains(patched, "POKKUM_PRERENDERED_DIR") {
		t.Errorf("prerendered-only handler was not patched: ok=%v src=%s", ok, patched)
	}

	both := `([serve(path.join(dir, 'client'), true), serve(path.join(dir, 'prerendered')), ssr].filter(Boolean))`
	patched, ok := applyHandlerPathPatches(both)
	if !ok {
		t.Fatal("combined handler was not patched")
	}
	if !strings.Contains(patched, "POKKUM_CLIENT_DIR") || !strings.Contains(patched, "POKKUM_PRERENDERED_DIR") {
		t.Errorf("combined handler is missing one of the two redirections:\n%s", patched)
	}
}
