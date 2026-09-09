package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Schema is a minimal JSON Schema (draft 2020-12) node — only the keywords
// this generator actually emits are represented, not a general-purpose
// schema-authoring type.
type Schema struct {
	SchemaURI            string             `json:"$schema,omitempty"`
	ID                   string             `json:"$id,omitempty"`
	Title                string             `json:"title,omitempty"`
	Description          string             `json:"description,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Const                any                `json:"const,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
}

// schemaID is this schema's own canonical URL. It does not need to resolve
// for the schema to be useful (editors identify a schema by the
// "$schema"/yaml-language-server association or a local path), but a stable
// $id is expected practice for a published JSON Schema.
const schemaID = "https://raw.githubusercontent.com/CreativeBeastDesign/Pokkum/main/schema/pokkum.schema.json"

// RenderSchema builds the complete schema for ports.ProjectConfig and
// marshals it as indented, newline-terminated JSON — the shape a checked-in
// generated file is expected to have.
func RenderSchema() ([]byte, error) {
	root := BuildSchema()
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("gen-schema: marshaling schema: %w", err)
	}
	return append(data, '\n'), nil
}

// BuildSchema walks ports.ProjectConfig by reflection (see reflectwalk.go)
// and attaches the root-level JSON Schema metadata. Exported so tests can
// call it directly without going through a temp-file round trip.
func BuildSchema() *Schema {
	root := schemaForType(reflect.TypeOf(ports.ProjectConfig{}))
	root.SchemaURI = "https://json-schema.org/draft/2020-12/schema"
	root.ID = schemaID
	root.Title = "Pokkum project configuration"
	root.Description = "JSON Schema for .pokkum.yaml (schema version " +
		strconv.Itoa(ports.ConfigSchemaVersion) + "), generated from " +
		"internal/ports/config.go by scripts/gen-schema. Do not hand-edit " +
		"schema/pokkum.schema.json; run `make schema`."
	return root
}
