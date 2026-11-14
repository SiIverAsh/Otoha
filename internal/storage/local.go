package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrTraversal = errors.New("storage: path escapes storage root")

// SafeJoin resolves relPath inside rootDir and rejects any `..` traversal escaping rootDir.
func SafeJoin(rootDir, relPath string) (string, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return "", err
	}
	joined := filepath.Clean(filepath.Join(absRoot, filepath.FromSlash(relPath)))

	rootCheck := absRoot
	joinCheck := joined
	if filepath.Separator == '\\' {
		rootCheck = strings.ToLower(rootCheck)
		joinCheck = strings.ToLower(joinCheck)
	}
	if joinCheck != rootCheck && !strings.HasPrefix(joinCheck, rootCheck+string(filepath.Separator)) {
		return "", ErrTraversal
	}
	return joined, nil
}

// FileExists reports whether path exists and is a regular file.
func FileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
