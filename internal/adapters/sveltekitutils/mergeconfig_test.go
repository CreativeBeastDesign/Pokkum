package sveltekitutils_test

import (
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
)

// A bare sveltekit() call is what a project has when its configuration lives in
// svelte.config.js — the majority of real SvelteKit projects.
const bareViteConfig = `import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()]
});
`

// TestTransformViteConfig_MergesSvelteConfigWhenInjectingIntoBareCall is the
// regression test for a reported build failure:
//
//	[vite-plugin-sveltekit-guard] Could not load virtual:env/dynamic/private:
//	To enable remote functions, add kit.experimental.remoteFunctions
//
// The project HAD that flag, in svelte.config.js. Injecting an adapter rewrote
// sveltekit() into sveltekit({ adapter: adapter() }), and SvelteKit calls
// load_svelte_config() only when the plugin argument is undefined — so supplying
// one to inject the adapter discarded the entire file. Aliases, csp, prerender
// options and every kit.experimental flag went with it.
func TestTransformViteConfig_MergesSvelteConfigWhenInjectingIntoBareCall(t *testing.T) {
	opts := sveltekitutils.DefaultInjectorOptions()
	opts.TargetAdapter = "@sveltejs/adapter-node"
	opts.UserSvelteConfigFile = "svelte.config.js"

	out, err := sveltekitutils.TransformViteConfig(bareViteConfig, opts)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}

	for _, want := range []string{
		// The file must actually be imported...
		"import __pokkumUserSvelteConfig from './svelte.config.js'",
		// ...its kit options separated from the rest...
		"kit: __pokkumKitOptions = {}",
		// ...and both spread into the call, with the adapter last so it wins.
		"...__pokkumSvelteRest",
		"...__pokkumKitOptions",
		"adapter: adapter()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated config missing %q; the project's svelte.config.js would be discarded.\nGot:\n%s", want, out)
		}
	}

	// Order is load-bearing: the adapter override has to come after the spreads,
	// or a project that sets its own adapter in svelte.config.js would win and
	// Pokkum would package output it cannot run.
	kitIdx := strings.Index(out, "...__pokkumKitOptions")
	adapterIdx := strings.LastIndex(out, "adapter: adapter()")
	if kitIdx < 0 || adapterIdx < kitIdx {
		t.Errorf("adapter override must come after the spread of the user's kit options, got:\n%s", out)
	}

	// A literal `kit:` key must NOT be handed to sveltekit(): split_config only
	// recognises flattened kit options, and would pass an unrecognised `kit` key
	// through to vite-plugin-svelte while still losing its contents.
	callStart := strings.Index(out, "sveltekit(")
	call := out[callStart:]
	if end := strings.Index(call, "]"); end > 0 {
		call = call[:end]
	}
	if strings.Contains(call, "kit:") && !strings.Contains(call, "__pokkumKitOptions") {
		t.Errorf("the sveltekit() argument must be flat, not nested under kit:, got:\n%s", call)
	}
}

// TestTransformViteConfig_NoSvelteConfigKeepsTheSimpleForm: with nothing to
// preserve, the merge machinery would be noise in the generated file.
func TestTransformViteConfig_NoSvelteConfigKeepsTheSimpleForm(t *testing.T) {
	opts := sveltekitutils.DefaultInjectorOptions()
	opts.TargetAdapter = "@sveltejs/adapter-node"
	opts.UserSvelteConfigFile = ""

	out, err := sveltekitutils.TransformViteConfig(bareViteConfig, opts)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if strings.Contains(out, "__pokkumUserSvelteConfig") {
		t.Errorf("no svelte config exists, so nothing should be imported:\n%s", out)
	}
	if !strings.Contains(out, "sveltekit({ adapter: adapter() })") {
		t.Errorf("expected the plain injected form:\n%s", out)
	}
}

// TestTransformViteConfig_ExistingOptionsAreNotMerged covers the third case: a
// project already passing options has itself opted out of svelte.config.js
// (SvelteKit warns about exactly this), so importing the file would change the
// project's own semantics rather than preserve them.
func TestTransformViteConfig_ExistingOptionsAreNotMerged(t *testing.T) {
	src := `import { sveltekit } from '@sveltejs/kit/vite';
import adapter from '@sveltejs/adapter-auto';
export default { plugins: [sveltekit({ adapter: adapter(), alias: { $lib: 'src/lib' } })] };
`
	opts := sveltekitutils.DefaultInjectorOptions()
	opts.TargetAdapter = "@sveltejs/adapter-node"
	opts.UserSvelteConfigFile = "svelte.config.js"

	out, err := sveltekitutils.TransformViteConfig(src, opts)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if strings.Contains(out, "__pokkumUserSvelteConfig") {
		t.Errorf("a config already passing options must not have svelte.config.js merged into it:\n%s", out)
	}
	// The project's own options must survive the adapter swap.
	if !strings.Contains(out, "$lib") {
		t.Errorf("existing options were dropped:\n%s", out)
	}
	if !strings.Contains(out, "adapter: adapter()") {
		t.Errorf("adapter was not injected:\n%s", out)
	}
}
