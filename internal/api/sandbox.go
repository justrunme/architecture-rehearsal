package api

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolveRoot returns a clean absolute path for the workspace root,
// evaluating symlinks when the directory exists (macOS /var → /private/var).
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// Root may not exist yet; resolve existing parent and rejoin.
	dir, base := filepath.Dir(abs), filepath.Base(abs)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, base), nil
	}
	return abs, nil
}

// sandboxPath resolves p under WorkDirRoot. Production servers must set WorkDirRoot.
// Protects against absolute escape, .., and symlink escape outside root.
func (s *Server) sandboxPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if s.WorkDirRoot == "" {
		return "", fmt.Errorf("workspace root required (pass --workdir)")
	}
	root, err := resolveRoot(s.WorkDirRoot)
	if err != nil {
		return "", err
	}

	var target string
	if filepath.IsAbs(p) {
		target = filepath.Clean(p)
	} else {
		clean := filepath.Clean(p)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path outside workspace root")
		}
		// Disallow absolute-looking after clean on Windows-style; on Unix Clean keeps relative.
		target = filepath.Join(root, clean)
	}
	target = filepath.Clean(target)

	// Resolve symlinks on existing path components.
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	} else {
		// File may not exist yet: resolve parent if present.
		dir, base := filepath.Dir(target), filepath.Base(target)
		if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
			target = filepath.Join(resolvedDir, base)
		}
	}

	if !underRoot(root, target) {
		return "", fmt.Errorf("path outside workspace root")
	}
	return target, nil
}

func underRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if target == root {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(root, sep) {
		// prefix check
		if strings.HasPrefix(target, root+sep) {
			return true
		}
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+sep) {
		return false
	}
	return true
}
