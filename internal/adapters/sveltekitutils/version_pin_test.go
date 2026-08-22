package sveltekitutils_test

import (
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
)

// VersionNamePinned decides whether Pokkum should warn that a build will not
// reproduce, so its bias matters more than its precision: a false negative is a
// warning the user cannot act on, a false positive merely restores the silence
// that shipped before. It is written to under-warn.
func TestVersionNamePinned_RecognisesRealPins(t *testing.T) {
	pinned := map[string]string{
		"pokkum's own injection":    `kit: { version: { name: process.env.SOURCE_DATE_EPOCH } }`,
		"literal string":            `kit: { version: { name: "1.4.2" } }`,
		"no spaces":                 `kit:{version:{name:pkg.version}}`,
		"multiline":                 "kit: {\n  version: {\n    name: commitSha,\n  },\n}",
		"epoch referenced anywhere": `const v = process.env.SOURCE_DATE_EPOCH ?? "dev";`,
	}
	for name, src := range pinned {
		t.Run(name, func(t *testing.T) {
			if !sveltekitutils.VersionNamePinned(src) {
				t.Errorf("should count as pinned, so no warning is emitted:\n%s", src)
			}
		})
	}
}

func TestVersionNamePinned_DetectsTheUnpinnedCase(t *testing.T) {
	// Forest's real config: adapter configured correctly, no version block at
	// all. This is the case that silently produced non-reproducible images.
	unpinned := `import adapter from "@sveltejs/adapter-static";
const config = {
  preprocess: vitePreprocess(),
  kit: { adapter: adapter() },
};
export default config;`

	if sveltekitutils.VersionNamePinned(unpinned) {
		t.Error("a config with no version block was treated as pinned; the build would silently not reproduce")
	}

	// A version block without a name is not a pin either — kit.version.pollInterval
	// exists and says nothing about the name.
	if sveltekitutils.VersionNamePinned(`kit: { version: { pollInterval: 5000 } }`) {
		t.Error("version.pollInterval was mistaken for a version name pin")
	}
}

// TestVersionNamePinned_ChecksEverySourceGiven covers the real call shape: the
// pin may live in svelte.config.js OR vite.config.ts, and finding it in either
// must suppress the warning.
func TestVersionNamePinned_ChecksEverySourceGiven(t *testing.T) {
	svelteCfg := `kit: { adapter: adapter() }`
	viteCfg := `sveltekit({ version: { name: process.env.SOURCE_DATE_EPOCH } })`

	if !sveltekitutils.VersionNamePinned(svelteCfg, viteCfg) {
		t.Error("a pin in the vite config was missed because the svelte config lacked one")
	}
	if sveltekitutils.VersionNamePinned("", "") {
		t.Error("two empty sources reported a pin")
	}
}
