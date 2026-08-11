package slsa

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Known lockfile filenames in order of preference for Bun / JS / Pokkum projects.
var lockfileNames = []string{
	"pokkum.lock",
	"bun.lock",
	"bun.lockb",
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
}

// inspectLockfiles scans projectDir for known package manager lockfiles and
// returns ResourceDescriptors containing their SHA256 digests.
func inspectLockfiles(projectDir string) ([]ports.ResourceDescriptor, error) {
	if projectDir == "" {
		return nil, nil
	}

	var descriptors []ports.ResourceDescriptor
	for _, name := range lockfileNames {
		path := filepath.Join(projectDir, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}

		hashHex, err := hashFile(path)
		if err != nil {
			return nil, fmt.Errorf("hash lockfile %s: %w", name, err)
		}

		descriptors = append(descriptors, ports.ResourceDescriptor{
			Name: name,
			URI:  "file://" + name,
			Digest: map[string]string{
				"sha256": hashHex,
			},
		})
	}

	return descriptors, nil
}

// hashFile computes the lowercase hex sha256 checksum of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
