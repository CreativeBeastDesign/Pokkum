package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-docs:", err)
		os.Exit(1)
	}
}

func run() error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	roadmapDir := filepath.Join(repoRoot, "docs", "roadmap")
	areas, err := LoadAreas(roadmapDir)
	if err != nil {
		return err
	}

	findingsPath := filepath.Join(repoRoot, findingsFromPath)
	findingsNumbers, err := ParseFindingsNumbers(findingsPath)
	if err != nil {
		return err
	}

	if errs := Validate(areas, repoRoot, findingsNumbers); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, "gen-docs: "+e.String())
		}
		return fmt.Errorf("%d validation error(s) in %s — fix docs/roadmap/*.yaml and re-run", len(errs), roadmapDir)
	}

	items := allItems(areas)
	allByID := make(map[string]Item, len(items))
	for _, it := range items {
		allByID[it.ID] = it
	}

	docsDir := filepath.Join(repoRoot, "docs")
	itemsDir := filepath.Join(docsDir, "items")
	if err := os.MkdirAll(itemsDir, 0o755); err != nil {
		return fmt.Errorf("gen-docs: creating %s: %w", itemsDir, err)
	}

	if err := writeFile(filepath.Join(docsDir, "Roadmap.md"), RenderRoadmap(areas)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(docsDir, "Shipped.md"), RenderShipped(areas)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(docsDir, "Features.md"), RenderFeatures(areas)); err != nil {
		return err
	}

	sortedItems := append([]Item{}, items...)
	sortByID(sortedItems)

	wantFiles := make(map[string]bool, len(sortedItems))
	for _, it := range sortedItems {
		name := it.ID + ".md"
		wantFiles[name] = true
		if err := writeFile(filepath.Join(itemsDir, name), RenderItemPage(it, allByID)); err != nil {
			return err
		}
	}

	if err := pruneStaleItemPages(itemsDir, wantFiles); err != nil {
		return err
	}

	return nil
}

func writeFile(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("gen-docs: writing %s: %w", path, err)
	}
	return nil
}

// pruneStaleItemPages removes docs/items/*.md pages left behind by an item
// id that no longer exists (e.g. renamed or deleted in docs/roadmap/*.yaml),
// so the items directory never accumulates orphaned generated pages. Only
// files that actually carry this tool's generatedMarker are ever removed —
// a page that doesn't isn't ours to delete.
func pruneStaleItemPages(itemsDir string, wantFiles map[string]bool) error {
	entries, err := os.ReadDir(itemsDir)
	if err != nil {
		return fmt.Errorf("gen-docs: reading %s: %w", itemsDir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if wantFiles[name] {
			continue
		}
		path := filepath.Join(itemsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("gen-docs: reading %s: %w", path, err)
		}
		if !bytes.Contains(data, []byte(generatedMarker)) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("gen-docs: removing stale generated page %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "gen-docs: removed stale generated item page %s (no matching item id)\n", name)
	}
	return nil
}

// findRepoRoot walks up from the working directory looking for go.mod, so
// this tool behaves the same whether invoked as `make docs` (from repo
// root) or `go run ./scripts/gen-docs` from any subdirectory.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("gen-docs: getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("gen-docs: could not locate repo root (no go.mod found above %s)", dir)
		}
		dir = parent
	}
}
