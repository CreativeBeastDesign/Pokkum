package main

import (
	"reflect"
	"sort"
	"strings"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// fieldKey identifies one Go struct field for the metadata overlay below:
// the declaring type plus the field's Go name. Keying by type rather than by
// a dotted path is deliberate — SecurityConfig.FailOnCVE, for instance, is
// reached both from ports.ProjectConfig.Security and ports.BuildProfile.
// Security, and it is the exact same field with the exact same constraint
// both times, so one entry here covers both call sites instead of drifting
// into two.
type fieldKey struct {
	Type reflect.Type
	Name string
}

// fieldInfo is what fieldMeta attaches to a field on top of what reflection
// alone can derive (its Go/YAML type, and whether it's required).
type fieldInfo struct {
	Description string
	Enum        []string
	// Const, when set, becomes the schema's "const" keyword. Only
	// ProjectConfig.Version uses this today.
	Const any
}

// fieldMeta annotates the handful of fields whose valid values are
// constrained beyond their Go type. Every enum value here is copied from a
// real typed constant, Valid() method, or Parse* function in internal/ports
// or internal/core — the exact same ones `pokkum config validate` and the
// real YAML loader use — never hand-typed from testdata/config/
// pokkum.yaml.golden or Vocabulary.md. Both of those are known to be
// incomplete for this purpose (see this package's doc comment in main.go).
//
// Sources, for anyone spot-checking these against the code:
//   - strategy:          internal/ports/compiler.go (StrategyLayered/
//     StrategyExe/StrategyStatic) and cmd/pokkum/config.go's
//     validateConfigFields, which accepts exactly these three.
//   - runtime:            internal/ports/runtime.go (RuntimeBun/RuntimeNode).
//   - base:                NOT an enum — see baseDescription below.
//   - security.fail_on_cve: internal/ports/scanner.go (SeverityLow/Medium/
//     High/Critical).
//   - sbom.format:        internal/ports/sbom.go (SBOMFormatSPDXJSON/
//     CycloneDXJSON/None).
//   - sbom.attach:        internal/ports/sbom.go (SBOMAttachReferrer/Tag/
//     Auto).
//   - cache.verify_mode:  internal/ports/cache.go's RemoteCacheVerifyMode.
//     Valid() (CacheVerifyAuto/StaticKey/Keyless/None).
//   - deploy.target:      internal/ports/deploy.go (DeployDokploy/
//     DeploySwiftwave).
//   - deploy.method:      internal/ports/deploy.go (DeployMethodAPI/
//     DeployMethodWebhook).
//   - output (profile only): internal/core/model.go's OutputMode constants
//     (OutputPush/OutputLocal/OutputTarball/OutputOCILayout).
var fieldMeta = buildFieldMeta()

// baseDescription documents ports.ProjectConfig.Base/ports.BuildProfile.Base,
// which is deliberately absent from fieldMeta's Enum lists above.
// core.ParseBaseImageSpec (internal/core/model.go) accepts either one of the
// three named presets OR an arbitrary image reference, which it treats as
// BaseImageCustom — so an "enum" restriction here would reject configs the
// real parser happily loads, failing this package's own
// "schema accepts what the parser accepts" test.
var baseDescription = "Base image tier: one of the built-in presets (\"" +
	string(ports.BaseImageDistroless) + "\", \"" + string(ports.BaseImageChainguard) + "\", \"" +
	string(ports.BaseImageDistrolessNode) + "\") or an explicit image reference (e.g. " +
	"\"registry/repo:tag\" or \"repo@sha256:...\"), which core.ParseBaseImageSpec treats " +
	"as a custom base. Not restricted to an enum here for that reason."

func buildFieldMeta() map[fieldKey]fieldInfo {
	pc := reflect.TypeOf(ports.ProjectConfig{})
	bp := reflect.TypeOf(ports.BuildProfile{})
	sec := reflect.TypeOf(ports.SecurityConfig{})
	sbom := reflect.TypeOf(ports.SBOMConfig{})
	cache := reflect.TypeOf(ports.CacheConfig{})
	deploy := reflect.TypeOf(ports.DeployConfig{})

	strategyEnum := []string{string(ports.StrategyLayered), string(ports.StrategyExe), string(ports.StrategyStatic)}
	runtimeEnum := []string{string(ports.RuntimeBun), string(ports.RuntimeNode)}

	return map[fieldKey]fieldInfo{
		{pc, "Version"}: {
			Description: "Configuration schema version.",
			Const:       ports.ConfigSchemaVersion,
		},
		{pc, "Strategy"}: {Description: "Packaging strategy.", Enum: strategyEnum},
		{bp, "Strategy"}: {Description: "Packaging strategy override for this profile.", Enum: strategyEnum},
		{pc, "Runtime"}:  {Description: "Application JS runtime. Empty means bun.", Enum: runtimeEnum},
		{bp, "Runtime"}:  {Description: "Application JS runtime override for this profile. Empty means inherit.", Enum: runtimeEnum},
		{pc, "Base"}:     {Description: baseDescription},
		{bp, "Base"}:     {Description: baseDescription},
		{bp, "Output"}: {
			Description: "Build output destination for this profile.",
			Enum: []string{
				string(core.OutputPush), string(core.OutputLocal),
				string(core.OutputTarball), string(core.OutputOCILayout),
			},
		},
		{sec, "FailOnCVE"}: {
			Description: "Fail the build if the resolved base image has a vulnerability at or above " +
				"this severity. Unset means warn-only.",
			Enum: []string{
				string(ports.SeverityLow), string(ports.SeverityMedium),
				string(ports.SeverityHigh), string(ports.SeverityCritical),
			},
		},
		{sbom, "Format"}: {
			Description: "SBOM serialisation format. \"none\" disables SBOM generation.",
			Enum: []string{
				string(ports.SBOMFormatSPDXJSON), string(ports.SBOMFormatCycloneDXJSON), string(ports.SBOMFormatNone),
			},
		},
		{sbom, "Attach"}: {
			Description: "How the SBOM artifact is attached to the OCI image.",
			Enum: []string{
				string(ports.SBOMAttachReferrer), string(ports.SBOMAttachTag), string(ports.SBOMAttachAuto),
			},
		},
		{cache, "VerifyMode"}: {
			Description: "Signature verification mode for remote build-cache hits.",
			Enum: []string{
				string(ports.CacheVerifyAuto), string(ports.CacheVerifyStaticKey),
				string(ports.CacheVerifyKeyless), string(ports.CacheVerifyNone),
			},
		},
		{deploy, "Target"}: {
			Description: "PaaS control plane to hand a successful push to. Empty disables deployment.",
			Enum:        []string{string(ports.DeployDokploy), string(ports.DeploySwiftwave)},
		},
		{deploy, "Method"}: {
			Description: "Transport used to drive the deploy target. Not every method is valid for " +
				"every target (core.ValidateDeployMethod owns that matrix); this schema does not " +
				"encode the cross-field rule.",
			Enum: []string{string(ports.DeployMethodAPI), string(ports.DeployMethodWebhook)},
		},
	}
}

// schemaForType converts a Go type into a Schema node. It handles exactly
// the kinds that appear in ports.ProjectConfig/ports.BuildProfile's field
// tree today; an unrecognized kind panics rather than silently emitting an
// empty schema, since that would be a generator bug (a new field of a new
// kind), not a malformed input — there is no untrusted input here.
func schemaForType(t reflect.Type) *Schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Slice:
		return &Schema{Type: "array", Items: schemaForType(t.Elem())}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic("gen-schema: unsupported map key kind " + t.Key().Kind().String() + " on " + t.String())
		}
		// A map's keys are never a fixed set (profile names, label names,
		// env var names, ...): every extra key is allowed as long as its
		// value matches the map's value type.
		return &Schema{Type: "object", AdditionalProperties: schemaForType(t.Elem())}
	case reflect.Struct:
		return schemaForStruct(t)
	default:
		panic("gen-schema: unsupported field kind " + t.Kind().String() + " on " + t.String())
	}
}

// schemaForStruct converts a config struct into an object Schema:
// additionalProperties is always false (the real loader uses yaml.v3's
// KnownFields(true), so an unrecognized key is a load error, not a value
// silently dropped — internal/adapters/config/config.go's Load doc comment
// spells this out), and a field is "required" exactly when its yaml tag has
// no "omitempty" option — the same rule ports.VEXExemptionConfig's own
// fields (cve/justification/expires/owner, all mandatory and all bare tags)
// already follow, so this falls out of the reflection walk rather than
// needing its own hardcoded list.
func schemaForStruct(t reflect.Type) *Schema {
	props := make(map[string]*Schema, t.NumField())
	var required []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" || name == "-" {
			continue
		}
		omitempty := false
		for _, opt := range parts[1:] {
			if opt == "omitempty" {
				omitempty = true
			}
		}

		fs := schemaForType(f.Type)
		if meta, ok := fieldMeta[fieldKey{t, f.Name}]; ok {
			if meta.Description != "" {
				fs.Description = meta.Description
			}
			if len(meta.Enum) > 0 {
				fs.Enum = append([]string{}, meta.Enum...)
			}
			if meta.Const != nil {
				fs.Const = meta.Const
			}
		}

		props[name] = fs
		if !omitempty {
			required = append(required, name)
		}
	}

	sort.Strings(required)

	return &Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: false,
	}
}
