package main

import "fmt"

// generatedMarker is the sentinel substring every generated file's header
// carries. main.go greps for it before deleting a stale docs/items/*.md
// page during pruning, so a file that happens to share a name with a
// removed item id but was NOT produced by this tool is never deleted.
const generatedMarker = "GENERATED — DO NOT EDIT BY HAND."

// generatedHeader renders the "generated, do not edit" banner every
// top-level output file starts with, naming its source and the exact
// command to regenerate it.
func generatedHeader(source string) string {
	return fmt.Sprintf(`<!--
%s
Source: %s
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

`, generatedMarker, source)
}
