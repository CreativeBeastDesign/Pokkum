package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadAreas reads every docs/roadmap/*.yaml file in dir, strictly decodes
// each into an Area, and stamps each item with the AreaName/SourceFile it
// came from. Files are processed in sorted (deterministic) order so that
// error messages and any file-order-dependent behavior are stable across
// runs and machines.
func LoadAreas(dir string) ([]Area, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("gen-docs: globbing %s: %w", dir, err)
	}
	sort.Strings(matches)

	areas := make([]Area, 0, len(matches))
	for _, path := range matches {
		area, err := loadAreaFile(path)
		if err != nil {
			return nil, err
		}
		areas = append(areas, area)
	}
	return areas, nil
}

func loadAreaFile(path string) (Area, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Area{}, fmt.Errorf("gen-docs: reading %s: %w", path, err)
	}

	var area Area
	dec := yaml.NewDecoder(bytes.NewReader(data))
	// Strict decoding: an unknown field (a typo'd key, a field from a
	// different schema version) fails loudly at load time instead of
	// silently vanishing — exactly the drift class this generator exists
	// to prevent. Mirrors internal/adapters/config's Load.
	dec.KnownFields(true)
	if err := dec.Decode(&area); err != nil {
		return Area{}, fmt.Errorf("gen-docs: parsing %s: %w", path, err)
	}

	relPath := path
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil {
			relPath = rel
		}
	}

	if strings.TrimSpace(area.Name) == "" {
		return Area{}, fmt.Errorf("gen-docs: %s: missing required top-level \"area\" display name", relPath)
	}

	for i := range area.Items {
		area.Items[i].AreaName = area.Name
		area.Items[i].SourceFile = relPath
	}
	return area, nil
}

// allItems flattens every item across every area, in file-then-declaration
// order (i.e. load order, not yet sorted for display — callers that need a
// deterministic display order must sort explicitly, per this repo's
// determinism convention).
func allItems(areas []Area) []Item {
	var out []Item
	for _, a := range areas {
		out = append(out, a.Items...)
	}
	return out
}
