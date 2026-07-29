package journal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalProjectRoot resolves one existing repository directory to the
// stable path shared by hook, worker, lock state, and journal storage.
func CanonicalProjectRoot(value string) (string, error) {
	root := strings.TrimSpace(value)
	if root == "" {
		return "", fmt.Errorf("project root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve project root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve project root symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect project root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root must be a directory")
	}
	return filepath.Clean(resolved), nil
}

// ResolveDatabasePath returns the exact absolute database path for a canonical
// repository root without creating or opening the database.
func ResolveDatabasePath(projectRoot string, value string) (string, error) {
	root, err := CanonicalProjectRoot(projectRoot)
	if err != nil {
		return "", err
	}
	databasePath := strings.TrimSpace(value)
	if databasePath == "" {
		databasePath = filepath.Join(root, filepath.FromSlash(defaultRelativePath))
	} else if !filepath.IsAbs(databasePath) {
		databasePath = filepath.Join(root, databasePath)
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	return filepath.Clean(absolute), nil
}
