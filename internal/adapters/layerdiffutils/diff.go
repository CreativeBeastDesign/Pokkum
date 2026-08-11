package layerdiffutils

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// IsUtilityPackage declares that layerdiffutils is a helper module and not a direct port adapter.
const IsUtilityPackage = true

// HeaderMismatch describes a metadata discrepancy between two tar entries.
type HeaderMismatch struct {
	Path     string `json:"path"`
	Field    string `json:"field"` // "mtime", "mode", "size", "uid", "gid"
	Value1   string `json:"value_1"`
	Value2   string `json:"value_2"`
}

// ContentMismatch describes a file payload checksum mismatch between two tar entries.
type ContentMismatch struct {
	Path    string `json:"path"`
	SHA256_1 string `json:"sha256_1"`
	SHA256_2 string `json:"sha256_2"`
}

// DiffResult holds the structural breakdown and cause analysis of a tar entry diff.
type DiffResult struct {
	Identical     bool              `json:"identical"`
	HeaderDiffs   []HeaderMismatch  `json:"header_diffs,omitempty"`
	ContentDiffs  []ContentMismatch `json:"content_diffs,omitempty"`
	AddedFiles    []string          `json:"added_files,omitempty"`
	RemovedFiles  []string          `json:"removed_files,omitempty"`
	ProbableCause string            `json:"probable_cause,omitempty"`
}

type tarEntry struct {
	header *tar.Header
	sha256 string
}

// CompareTarStreams walks two tar readers entry-by-entry and computes metadata and content diffs.
func CompareTarStreams(r1, r2 io.Reader) (*DiffResult, error) {
	entries1, err := readTarMap(r1)
	if err != nil {
		return nil, fmt.Errorf("layerdiffutils adapter: failed reading tar 1: %w", err)
	}

	entries2, err := readTarMap(r2)
	if err != nil {
		return nil, fmt.Errorf("layerdiffutils adapter: failed reading tar 2: %w", err)
	}

	res := &DiffResult{Identical: true}

	// Check entries in tar 1 against tar 2
	for path, e1 := range entries1 {
		e2, ok := entries2[path]
		if !ok {
			res.Identical = false
			res.RemovedFiles = append(res.RemovedFiles, path)
			continue
		}

		// Compare Headers
		if !e1.header.ModTime.Equal(e2.header.ModTime) {
			res.Identical = false
			res.HeaderDiffs = append(res.HeaderDiffs, HeaderMismatch{
				Path:   path,
				Field:  "mtime",
				Value1: e1.header.ModTime.String(),
				Value2: e2.header.ModTime.String(),
			})
		}
		if e1.header.Mode != e2.header.Mode {
			res.Identical = false
			res.HeaderDiffs = append(res.HeaderDiffs, HeaderMismatch{
				Path:   path,
				Field:  "mode",
				Value1: fmt.Sprintf("%o", e1.header.Mode),
				Value2: fmt.Sprintf("%o", e2.header.Mode),
			})
		}
		if e1.header.Size != e2.header.Size {
			res.Identical = false
			res.HeaderDiffs = append(res.HeaderDiffs, HeaderMismatch{
				Path:   path,
				Field:  "size",
				Value1: fmt.Sprintf("%d", e1.header.Size),
				Value2: fmt.Sprintf("%d", e2.header.Size),
			})
		}

		// Compare Content Hashes
		if e1.sha256 != e2.sha256 {
			res.Identical = false
			res.ContentDiffs = append(res.ContentDiffs, ContentMismatch{
				Path:     path,
				SHA256_1: e1.sha256,
				SHA256_2: e2.sha256,
			})
		}
	}

	// Check for added files in tar 2
	for path := range entries2 {
		if _, ok := entries1[path]; !ok {
			res.Identical = false
			res.AddedFiles = append(res.AddedFiles, path)
		}
	}

	// Compute Heuristics
	if !res.Identical {
		res.ProbableCause = deduceCause(res)
	}

	return res, nil
}

func readTarMap(r io.Reader) (map[string]tarEntry, error) {
	tr := tar.NewReader(r)
	entries := make(map[string]tarEntry)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		h := sha256.New()
		if _, err := io.Copy(h, tr); err != nil {
			return nil, err
		}

		entries[hdr.Name] = tarEntry{
			header: hdr,
			sha256: hex.EncodeToString(h.Sum(nil)),
		}
	}

	return entries, nil
}

func deduceCause(res *DiffResult) string {
	if len(res.HeaderDiffs) > 0 {
		for _, hd := range res.HeaderDiffs {
			if hd.Field == "mtime" {
				return "Timestamp non-determinism detected (mtime headers not pinned to SOURCE_DATE_EPOCH)."
			}
		}
	}

	if len(res.ContentDiffs) > 0 {
		for _, cd := range res.ContentDiffs {
			if strings.Contains(cd.Path, "assets.generated.ts") || strings.Contains(cd.Path, "manifest") {
				return "Unsorted map iteration or bundle code-generation drift."
			}
			if strings.Contains(cd.Path, ".json") || strings.Contains(cd.Path, ".js") {
				return "Build-time code injection or host path embedding."
			}
		}
	}

	return "Unknown artifact drift between builds."
}
