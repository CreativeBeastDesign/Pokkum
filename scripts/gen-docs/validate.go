package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ValidationError is one concrete, actionable problem found in the loaded
// roadmap source. Validate returns every error it finds, not just the
// first, so a maintainer fixing a batch of yaml files gets the full list in
// one pass.
type ValidationError struct {
	File string
	ID   string
	Msg  string
}

func (e ValidationError) String() string {
	switch {
	case e.File != "" && e.ID != "":
		return fmt.Sprintf("%s: item %q: %s", e.File, e.ID, e.Msg)
	case e.ID != "":
		return fmt.Sprintf("item %q: %s", e.ID, e.Msg)
	case e.File != "":
		return fmt.Sprintf("%s: %s", e.File, e.Msg)
	default:
		return e.Msg
	}
}

// Validate checks every loaded item against the schema's required fields,
// enumerated values, cross-references, and on-disk impl paths. repoRoot is
// used to resolve impl paths; findingsNumbers is the set of entry numbers
// that genuinely exist in overnight-findings.md (see ParseFindingsNumbers).
//
// Two full passes are used deliberately: pass one only collects every
// item's id (to detect duplicates and to build the id->item lookup table
// referenced/related validation needs); pass two runs the actual per-item
// checks. Related-id validation could not run correctly in a single pass,
// since an item can legitimately reference an id defined in a file loaded
// after it — LoadAreas's sorted-file-order does not imply forward
// references are wrong.
func Validate(areas []Area, repoRoot string, findingsNumbers map[int]bool) []ValidationError {
	var errs []ValidationError

	idFiles := map[string][]string{} // id -> every source file it was declared in
	allByID := map[string]Item{}
	for _, area := range areas {
		for _, it := range area.Items {
			if strings.TrimSpace(it.ID) == "" {
				errs = append(errs, ValidationError{File: it.SourceFile, Msg: fmt.Sprintf("item titled %q is missing required \"id\"", it.Title)})
				continue
			}
			idFiles[it.ID] = append(idFiles[it.ID], it.SourceFile)
			allByID[it.ID] = it
		}
	}

	for id, files := range idFiles {
		if len(files) > 1 {
			sorted := append([]string{}, files...)
			sort.Strings(sorted)
			errs = append(errs, ValidationError{
				ID:  id,
				Msg: fmt.Sprintf("duplicate id declared in multiple files: %s", strings.Join(sorted, ", ")),
			})
		}
	}

	for _, area := range areas {
		for _, it := range area.Items {
			if strings.TrimSpace(it.ID) == "" {
				continue // already reported above
			}
			errs = append(errs, validateItem(it, repoRoot, allByID, findingsNumbers)...)
		}
	}

	// Deterministic output ordering: never rely on map iteration order
	// (mem:core's determinism invariant applies to this tool's own
	// output, not just OCI artifacts).
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		if errs[i].ID != errs[j].ID {
			return errs[i].ID < errs[j].ID
		}
		return errs[i].Msg < errs[j].Msg
	})
	return errs
}

func validateItem(it Item, repoRoot string, allByID map[string]Item, findingsNumbers map[int]bool) []ValidationError {
	var errs []ValidationError
	add := func(format string, args ...any) {
		errs = append(errs, ValidationError{File: it.SourceFile, ID: it.ID, Msg: fmt.Sprintf(format, args...)})
	}

	if strings.TrimSpace(it.Title) == "" {
		add("missing required \"title\"")
	}
	if strings.TrimSpace(it.Status) == "" {
		add("missing required \"status\"")
	} else if !validStatuses[it.Status] {
		add("unknown status %q (want one of: open, in-progress, awaiting-decision, shipped, wont-do)", it.Status)
	}
	if strings.TrimSpace(it.Summary) == "" {
		add("missing required \"summary\"")
	}

	if it.Kind != "" && !validKinds[it.Kind] {
		add("unknown kind %q (want one of: feature, hardening, fix, dx, infra)", it.Kind)
	}
	if it.Tier != "" && !validTiers[it.Tier] {
		add("unknown tier %q (want one of: moat, foundation, polish, non-goal)", it.Tier)
	}
	if it.Stage != "" && !isValidStage(it.Stage) {
		add("unknown stage %q (want one of: v1.1, v1.2, v2.0, backlog)", it.Stage)
	}

	for _, path := range it.Impl {
		full := filepath.Join(repoRoot, path)
		if _, err := os.Stat(full); err != nil {
			add("impl path does not exist on disk: %s", path)
		}
	}

	// A [title](item:<id>) reference in authored prose is resolved to a real
	// relative path at render time, so a typo'd id would ship as a dead link
	// with nothing else to catch it.
	for _, text := range freeTextFields(it) {
		for _, ref := range itemRefIDs(text) {
			if _, ok := allByID[ref]; !ok {
				add("item: reference to unknown id %q", ref)
			}
		}
	}

	for _, rel := range it.Related {
		if _, ok := allByID[rel]; !ok {
			add("related references unknown item id %q", rel)
		}
	}

	for _, n := range it.Evidence.Findings {
		if !findingsNumbers[n] {
			add("evidence.findings references overnight-findings.md entry #%d, which does not exist", n)
		}
	}

	return errs
}
