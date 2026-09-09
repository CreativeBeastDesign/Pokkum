package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// ---------------------------------------------------------------------------
// Field parity guard.
//
// This is the load-bearing test: a new .pokkum.yaml field must not be able
// to ship with schema/pokkum.schema.json silently incomplete. Modeled
// directly on cmd/pokkum/guide_test.go's TestGuideDocumentsEveryConfigField
// and its yamlFieldNames helper — same reflection walk, same idea, checked
// against the generated schema tree instead of the guide's prose.
// ---------------------------------------------------------------------------

// yamlFieldNames walks a struct type recursively and returns every name
// declared in a `yaml:` tag, skipping "-" and inline/omitempty modifiers.
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

// collectSchemaPropertyNames walks a Schema tree and returns every property
// name that appears anywhere in it (at any depth, under "properties" or
// inside "items"/"additionalProperties" schemas).
func collectSchemaPropertyNames(s *Schema, out map[string]bool) {
	if s == nil {
		return
	}
	for name, sub := range s.Properties {
		out[name] = true
		collectSchemaPropertyNames(sub, out)
	}
	collectSchemaPropertyNames(s.Items, out)
	if sub, ok := s.AdditionalProperties.(*Schema); ok {
		collectSchemaPropertyNames(sub, out)
	}
}

// TestSchemaDocumentsEveryConfigField is the load-bearing guard: a new
// .pokkum.yaml field cannot ship with schema/pokkum.schema.json silently
// incomplete.
func TestSchemaDocumentsEveryConfigField(t *testing.T) {
	fields := map[string]bool{}
	seen := map[reflect.Type]bool{}
	yamlFieldNames(reflect.TypeOf(ports.ProjectConfig{}), seen, fields)
	yamlFieldNames(reflect.TypeOf(ports.BuildProfile{}), seen, fields)

	// Premise check: a scan that finds nothing would make this test pass
	// while verifying nothing at all.
	if len(fields) < 30 {
		t.Fatalf("[TEST SETUP] reflection found only %d yaml fields across ports.ProjectConfig and "+
			"ports.BuildProfile; the walk has gone blind", len(fields))
	}
	for _, must := range []string{"repo", "require_env", "fail_on_cve", "update_image", "profiles"} {
		if !fields[must] {
			t.Fatalf("[TEST SETUP] reflection did not find known field %q; the walk is wrong", must)
		}
	}

	schemaFields := map[string]bool{}
	collectSchemaPropertyNames(BuildSchema(), schemaFields)

	// Premise check on the schema side too: BuildSchema returning an empty
	// tree would otherwise make every field look "missing" for a boring,
	// uninteresting reason.
	if len(schemaFields) < 30 {
		t.Fatalf("[TEST SETUP] BuildSchema() produced only %d property names; the generator is not walking "+
			"the config types", len(schemaFields))
	}

	var missing []string
	for name := range fields {
		if !schemaFields[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("schema/pokkum.schema.json does not mention %d .pokkum.yaml field(s): %v\n"+
			"\tEvery yaml field of ports.ProjectConfig/ports.BuildProfile must appear somewhere in the\n"+
			"\tgenerated schema. Run `make schema` after adding a field to internal/ports/config.go, or\n"+
			"\tadd it to schemaForType/schemaForStruct in reflectwalk.go if the walk itself is missing it.",
			len(missing), missing)
	}
	t.Logf("checked %d yaml fields against the generated schema", len(fields))
}

// ---------------------------------------------------------------------------
// A small, focused JSON-Schema-shaped validator.
//
// This project is deliberately zero-dependency, so rather than pull in a
// JSON Schema validation library to test a JSON Schema generator, this
// implements exactly the keywords BuildSchema emits (type, enum, const,
// properties, required, additionalProperties, items) against the native Go
// values yaml.v3 decodes into — no JSON round trip needed, since yaml.v3
// unmarshals mappings into map[string]interface{} and sequences into
// []interface{}, which is already the shape a decoded JSON document would
// take.
// ---------------------------------------------------------------------------

// validate checks node against s, returning every violation found (rather
// than stopping at the first) so a test failure shows the whole picture.
// path is a human-readable location for error messages ("$" for the root).
func validate(node any, s *Schema, path string) []string {
	if s == nil {
		return nil
	}

	var errs []string

	switch s.Type {
	case "object":
		m, ok := node.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected an object, got %T", path, node)}
		}
		for _, req := range s.Required {
			if _, ok := m[req]; !ok {
				errs = append(errs, fmt.Sprintf("%s: missing required property %q", path, req))
			}
		}
		for key, val := range m {
			sub, known := s.Properties[key]
			switch ap := s.AdditionalProperties.(type) {
			case bool:
				if !ap && !known {
					errs = append(errs, fmt.Sprintf("%s: unknown property %q (not permitted by additionalProperties: false)", path, key))
					continue
				}
			case *Schema:
				if !known {
					errs = append(errs, validate(val, ap, path+"."+key)...)
					continue
				}
			}
			if known {
				errs = append(errs, validate(val, sub, path+"."+key)...)
			}
		}
		return errs

	case "array":
		arr, ok := node.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected an array, got %T", path, node)}
		}
		for i, item := range arr {
			errs = append(errs, validate(item, s.Items, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return errs

	case "string":
		str, ok := node.(string)
		if !ok {
			return []string{fmt.Sprintf("%s: expected a string, got %T", path, node)}
		}
		if len(s.Enum) > 0 && !containsString(s.Enum, str) {
			errs = append(errs, fmt.Sprintf("%s: value %q is not one of %v", path, str, s.Enum))
		}
		return errs

	case "integer":
		switch v := node.(type) {
		case int, int64, uint64:
			if s.Const != nil && fmt.Sprint(v) != fmt.Sprint(s.Const) {
				errs = append(errs, fmt.Sprintf("%s: value %v does not equal const %v", path, v, s.Const))
			}
		default:
			errs = append(errs, fmt.Sprintf("%s: expected an integer, got %T", path, node))
		}
		return errs

	case "boolean":
		if _, ok := node.(bool); !ok {
			return []string{fmt.Sprintf("%s: expected a boolean, got %T", path, node)}
		}
		return nil

	default:
		return []string{fmt.Sprintf("%s: [TEST SETUP] validator does not know schema type %q", path, s.Type)}
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// decodeYAML parses raw YAML into the map[string]any/[]any/... shape
// validate expects.
func decodeYAML(t *testing.T, data []byte) any {
	t.Helper()
	var out any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding YAML: %v", err)
	}
	return out
}

func goldenPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "config", "pokkum.yaml.golden")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("[TEST SETUP] cannot find golden fixture at %s: %v", p, err)
	}
	return p
}

// TestGoldenFixtureValidatesAgainstSchema asserts the schema accepts what
// the real parser accepts: testdata/config/pokkum.yaml.golden must validate
// cleanly, including its "local" and "production" profiles.
func TestGoldenFixtureValidatesAgainstSchema(t *testing.T) {
	data, err := os.ReadFile(goldenPath(t))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading golden fixture: %v", err)
	}
	doc := decodeYAML(t, data)

	errs := validate(doc, BuildSchema(), "$")
	if len(errs) > 0 {
		t.Errorf("golden fixture failed schema validation (%d issue(s)):", len(errs))
		for _, e := range errs {
			t.Errorf("  - %s", e)
		}
	}
}

// TestSchemaRejectsUnknownTopLevelKey asserts the schema rejects what the
// parser rejects: internal/adapters/config/config.go's Load uses yaml.v3's
// KnownFields(true), so a typo'd or unknown top-level key is a load error,
// not a silently ignored one — additionalProperties: false must say the
// same thing.
func TestSchemaRejectsUnknownTopLevelKey(t *testing.T) {
	data, err := os.ReadFile(goldenPath(t))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading golden fixture: %v", err)
	}
	doc := decodeYAML(t, data).(map[string]any)
	doc["totally_not_a_real_field"] = true

	errs := validate(doc, BuildSchema(), "$")
	if !anyContains(errs, "totally_not_a_real_field") {
		t.Errorf("expected a validation error naming the unknown top-level key, got: %v", errs)
	}
}

// TestSchemaRejectsUnknownProfileKey mirrors the above one level down: a
// profile is itself validated with additionalProperties: false (it goes
// through the exact same yaml.v3 KnownFields(true) decode as the top-level
// config), so an unknown key nested in profiles.<name> must be rejected too.
func TestSchemaRejectsUnknownProfileKey(t *testing.T) {
	data, err := os.ReadFile(goldenPath(t))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading golden fixture: %v", err)
	}
	doc := decodeYAML(t, data).(map[string]any)
	profiles, ok := doc["profiles"].(map[string]any)
	if !ok {
		t.Fatalf("[TEST SETUP] golden fixture has no profiles map; the guard cannot run")
	}
	local, ok := profiles["local"].(map[string]any)
	if !ok {
		t.Fatalf("[TEST SETUP] golden fixture has no profiles.local; the guard cannot run")
	}
	local["totally_not_a_real_profile_field"] = true

	errs := validate(doc, BuildSchema(), "$")
	if !anyContains(errs, "totally_not_a_real_profile_field") {
		t.Errorf("expected a validation error naming the unknown profiles.local key, got: %v", errs)
	}
}

// TestSchemaRejectsInvalidStrategyValue asserts strategy's enum is actually
// enforced: cmd/pokkum/config.go's validateConfigFields (and, before it ever
// gets that far, core's own packaging strategy validation) rejects any value
// outside layered/exe/static.
func TestSchemaRejectsInvalidStrategyValue(t *testing.T) {
	data, err := os.ReadFile(goldenPath(t))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading golden fixture: %v", err)
	}
	doc := decodeYAML(t, data).(map[string]any)
	doc["strategy"] = "quantum-tunneling"

	errs := validate(doc, BuildSchema(), "$")
	if !anyContains(errs, "quantum-tunneling") {
		t.Errorf("expected a validation error naming the invalid strategy value, got: %v", errs)
	}
}

func anyContains(errs []string, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}
