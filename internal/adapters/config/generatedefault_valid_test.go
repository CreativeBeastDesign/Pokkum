package config_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestGenerateDefault_EveryEnumValuedFieldParses is the regression test for a
// bug found by simply running the tool: `pokkum init` wrote
// `sbom.attach: attestation` into .pokkum.yaml, and `pokkum build` then refused
// to start with `invalid sbom attach mode`. The very first two commands a new
// user runs did not work together.
//
// "attestation" is a reasonable-sounding word — SBOMs really are attached as
// attestations — but it was never one of the mode's values (referrer, tag,
// auto). Both halves were individually correct and tested: GenerateDefault had
// tests asserting its fields, and ParseSBOMAttachMode had tests rejecting bad
// input. Nothing tested across the seam, so a value that satisfied neither
// contract sat in the generated config indefinitely.
//
// This test closes the class rather than the instance: every enum-valued field
// GenerateDefault emits is pushed through the same parser the build path
// applies to it. A new enum-valued config field is only covered here once it is
// added below, so the sibling test asserting the field count is what forces
// that — see TestGenerateDefault_EnumFieldCoverageIsComplete.
func TestGenerateDefault_EveryEnumValuedFieldParses(t *testing.T) {
	m, err := config.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("config.New: %v", err)
	}

	// Cover every combination of the choices `pokkum init` actually offers, not
	// just the defaults — a wrong value reachable only via a non-default answer
	// is still a broken first run for whoever picks it.
	// Exactly the presets pokkum init now offers, plus "" for the default.
	// "chainguard-static" was offered by the prompt and is not a preset at all
	// (an unimplemented roadmap item), which this test caught — see the
	// corrected list in cmd/pokkum/init.go's promptChoice call.
	bases := []string{"", "distroless", "chainguard", "distroless-node"}
	strategies := []string{"", "layered", "static"}
	cves := []string{"", "none", "low", "medium", "high", "critical"}

	for _, base := range bases {
		for _, strategy := range strategies {
			for _, cve := range cves {
				for _, local := range []bool{false, true} {
					cfg := m.GenerateDefault(ports.InitConfigOptions{
						Repo:               "ghcr.io/example/app",
						BasePreset:         base,
						Strategy:           strategy,
						FailOnCVE:          cve,
						EnableLocalProfile: local,
					})
					checkConfigEnums(t, cfg, base, strategy, cve, local)
				}
			}
		}
	}
}

// checkConfigEnums pushes each enum-valued field through the parser the build
// path uses for it. An empty value is skipped deliberately: empty means "unset,
// take the documented default", which the build path handles separately.
func checkConfigEnums(t *testing.T, cfg *ports.ProjectConfig, base, strategy, cve string, local bool) {
	t.Helper()
	ctx := func(field string) string {
		return "GenerateDefault(base=" + base + ", strategy=" + strategy + ", cve=" + cve + ")." + field
	}

	if cfg.SBOM.Attach != "" {
		if _, err := core.ParseSBOMAttachMode(cfg.SBOM.Attach); err != nil {
			t.Errorf("%s = %q, which pokkum build refuses: %v", ctx("sbom.attach"), cfg.SBOM.Attach, err)
		}
	}
	if cfg.SBOM.Format != "" {
		if _, err := core.ParseSBOMFormat(cfg.SBOM.Format); err != nil {
			t.Errorf("%s = %q, which pokkum build refuses: %v", ctx("sbom.format"), cfg.SBOM.Format, err)
		}
	}
	if cfg.Strategy != "" && !ports.BuildStrategy(cfg.Strategy).Valid() {
		t.Errorf("%s = %q, which is not a valid ports.BuildStrategy", ctx("strategy"), cfg.Strategy)
	}
	if cfg.Base != "" {
		if _, err := core.ParseBaseImagePreset(cfg.Base); err != nil {
			t.Errorf("%s = %q, which pokkum build refuses: %v", ctx("base"), cfg.Base, err)
		}
	}

	for name, p := range cfg.Profiles {
		if p.SBOM.Attach != "" {
			if _, err := core.ParseSBOMAttachMode(p.SBOM.Attach); err != nil {
				t.Errorf("%s profile %q sbom.attach = %q, which pokkum build refuses: %v", ctx(""), name, p.SBOM.Attach, err)
			}
		}
		if p.SBOM.Format != "" {
			if _, err := core.ParseSBOMFormat(p.SBOM.Format); err != nil {
				t.Errorf("%s profile %q sbom.format = %q, which pokkum build refuses: %v", ctx(""), name, p.SBOM.Format, err)
			}
		}
		if p.Strategy != "" && !ports.BuildStrategy(p.Strategy).Valid() {
			t.Errorf("%s profile %q strategy = %q, which is not a valid ports.BuildStrategy", ctx(""), name, p.Strategy)
		}
		if p.Output != "" {
			if _, err := core.ParseOutputMode(p.Output); err != nil {
				t.Errorf("%s profile %q output = %q, which pokkum build refuses: %v", ctx(""), name, p.Output, err)
			}
		}
	}
}
