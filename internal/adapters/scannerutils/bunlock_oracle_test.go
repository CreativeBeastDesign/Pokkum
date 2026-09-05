package scannerutils

import (
	"encoding/json"
	"sort"
	"strings"
)

// This file holds a pinned, independent copy of ParseBunLock's logic, used
// as a differential reference by bunlock_differential_test.go and
// FuzzParseBunLock.
//
// Why a second copy of the same algorithm is worth having here: this parser
// has had two separate incidents recorded in Lessons.md -- it could not read
// a real lockfile at all (2026-09-01), and it chose duplicate-name winners by
// map iteration order, emitting six different SBOM digests from six builds of
// unchanged source (2026-08-22). Both were behaviour changes with no failing
// assertion anywhere, because the unit tests pin a handful of hand-written
// shapes rather than the parser's whole answer. A pinned reference plus a
// fuzz differential turns "did this edit change any answer, on any input"
// into a question a test can answer.
//
// It was written as the before-side of a typed-decode rewrite that was
// measured and then reverted (see ParseBunLock's own notes); it is kept
// because the harness it anchors outlived the rewrite.
//
// Rules for this file: do not refactor it, do not share helpers with the
// production code beyond the ones it already calls, and do not edit it to
// make a failing differential pass. A disagreement means the production
// parser's answer moved; decide deliberately whether that was intended, and
// only then update this copy in the same commit, saying why.

func parseBunLockOracle(data []byte) ([]CatalogPackage, error) {
	var parsed struct {
		Workspaces map[string]bunWorkspace `json:"workspaces"`
		Packages   map[string]any          `json:"packages"`
	}
	if err := json.Unmarshal(stripJSONTrailingCommas(data), &parsed); err != nil {
		return nil, err
	}

	prodRoots := make(map[string]bool)
	devRoots := make(map[string]bool)
	for _, ws := range parsed.Workspaces {
		for name := range ws.Dependencies {
			prodRoots[name] = true
		}
		for name := range ws.DevDependencies {
			devRoots[name] = true
		}
	}

	type parsedEntry struct {
		name, version string
		hoisted       bool
	}
	entries := make(map[string]parsedEntry, len(parsed.Packages))
	graph := make(map[string][]string, len(parsed.Packages))

	keys := make([]string, 0, len(parsed.Packages))
	for k := range parsed.Packages {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if k == "" {
			continue
		}
		v := parsed.Packages[k]
		name, version := parseBunPackageEntryOracle(k, v)
		if name == "" || version == "" {
			continue
		}
		hoisted := k == name
		if existing, ok := entries[name]; ok && (existing.hoisted || !hoisted) {
			continue
		}
		entries[name] = parsedEntry{name: name, version: version, hoisted: hoisted}
		graph[name] = bunPackageDependencyNamesOracle(v)
	}

	prodReachable := bunReachableFrom(prodRoots, graph)
	devReachable := bunReachableFrom(devRoots, graph)

	packages := make([]CatalogPackage, 0, len(entries))
	for _, e := range entries {
		scope := ScopeUnknown
		switch {
		case prodReachable[e.name]:
			scope = ScopeProduction
		case devReachable[e.name]:
			scope = ScopeDevelopment
		}
		packages = append(packages, CatalogPackage{
			Name:      e.name,
			Version:   e.version,
			Type:      PkgTypeNpm,
			Ecosystem: "npm",
			Resolved:  true,
			Scope:     scope,
		})
	}

	return packages, nil
}

func parseBunPackageEntryOracle(key string, entry any) (name, version string) {
	if arr, ok := entry.([]any); ok && len(arr) > 0 {
		if idStr, ok := arr[0].(string); ok {
			lastAt := strings.LastIndex(idStr, "@")
			if lastAt > 0 {
				return idStr[:lastAt], idStr[lastAt+1:]
			}
		}
	}
	lastAt := strings.LastIndex(key, "@")
	if lastAt > 0 {
		return key[:lastAt], key[lastAt+1:]
	}
	return key, ""
}

func bunPackageDependencyNamesOracle(entry any) []string {
	arr, ok := entry.([]any)
	if !ok || len(arr) < 3 {
		return nil
	}
	meta, ok := arr[2].(map[string]any)
	if !ok {
		return nil
	}
	var names []string
	for _, key := range []string{"dependencies", "optionalDependencies", "peerDependencies"} {
		deps, ok := meta[key].(map[string]any)
		if !ok {
			continue
		}
		for name := range deps {
			names = append(names, name)
		}
	}
	return names
}
