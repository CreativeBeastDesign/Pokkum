package internal_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHexagonalArchitecturePurity verifies Hexagonal Architecture invariants
// across internal/ports, internal/core, and internal/adapters.
func TestHexagonalArchitecturePurity(t *testing.T) {
	rootPath, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("Failed to resolve current directory: %v", err)
	}

	err = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories, non-go files, and test files
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, relErr := filepath.Rel(rootPath, path)
		if relErr != nil {
			return relErr
		}

		// Normalize paths to forward slashes for cross-platform checking
		normalizedRel := filepath.ToSlash(relPath)
		dirPath := filepath.Dir(normalizedRel)

		fset := token.NewFileSet()
		node, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("Failed to parse file %s: %v", relPath, parseErr)
			return nil
		}

		for _, imp := range node.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)

			// Invariant 1: internal/ports must NEVER import core, adapters, or cmd
			if dirPath == "ports" || strings.HasPrefix(dirPath, "ports/") {
				if strings.Contains(importPath, "/internal/core") {
					t.Errorf("[HEXAGONAL VIOLATION] %s: leaf port package cannot import internal/core (%s)", relPath, importPath)
				}
				if strings.Contains(importPath, "/internal/adapters") {
					t.Errorf("[HEXAGONAL VIOLATION] %s: leaf port package cannot import concrete adapters (%s)", relPath, importPath)
				}
				if strings.Contains(importPath, "/cmd") {
					t.Errorf("[HEXAGONAL VIOLATION] %s: leaf port package cannot import cmd (%s)", relPath, importPath)
				}
			}

			// Invariant 2: internal/core must NEVER import concrete adapters or cmd
			if dirPath == "core" || strings.HasPrefix(dirPath, "core/") {
				if strings.Contains(importPath, "/internal/adapters") {
					t.Errorf("[HEXAGONAL VIOLATION] %s: domain core cannot import concrete adapters (%s)", relPath, importPath)
				}
				if strings.Contains(importPath, "/cmd") {
					t.Errorf("[HEXAGONAL VIOLATION] %s: domain core cannot import cmd (%s)", relPath, importPath)
				}
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("Failed walking internal directory: %v", err)
	}
}

// TestUtilityPackageNamingConvention verifies that non-adapter helper packages
// located in internal/adapters/ adhere to the 'utils' suffix convention.
func TestUtilityPackageNamingConvention(t *testing.T) {
	adaptersDir, err := filepath.Abs("adapters")
	if err != nil {
		t.Fatalf("Failed to resolve adapters directory: %v", err)
	}

	entries, err := os.ReadDir(adaptersDir)
	if err != nil {
		t.Fatalf("Failed to read adapters directory: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirName := entry.Name()
		pkgDir := filepath.Join(adaptersDir, dirName)

		// Check if package contains 'const IsUtilityPackage = true'
		isUtilPkg := false
		fset := token.NewFileSet()
		pkgs, parseErr := parser.ParseDir(fset, pkgDir, nil, parser.ParseComments)
		if parseErr != nil {
			continue
		}

		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				ast.Inspect(file, func(n ast.Node) bool {
					valueSpec, ok := n.(*ast.ValueSpec)
					if !ok {
						return true
					}
					for _, name := range valueSpec.Names {
						if name.Name == "IsUtilityPackage" {
							isUtilPkg = true
						}
					}
					return true
				})
			}
		}

		if isUtilPkg && !strings.HasSuffix(dirName, "utils") {
			t.Errorf("[CONVENTION VIOLATION] Utility package directory '%s' declares IsUtilityPackage = true but lacks 'utils' suffix", dirName)
		}
	}
}
