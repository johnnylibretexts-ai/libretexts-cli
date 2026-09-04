package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveWriteRoot(root string) (string, error) {
	if root == "" {
		return "", invalidWritePath("write root must not be empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", filesystemError(err)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", filesystemError(err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", filesystemError(err)
	}
	return resolved, nil
}

func confinedOutputPath(root, requested string) (string, error) {
	if requested == "" {
		return "", invalidWritePath("output path must not be empty")
	}
	if filepath.IsAbs(requested) {
		return "", invalidWritePath(fmt.Sprintf("output path must be relative: %q", requested))
	}

	cleaned := filepath.Clean(requested)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", invalidWritePath(fmt.Sprintf("output path escapes the write root: %q", requested))
	}

	candidate := root
	for _, segment := range strings.Split(cleaned, string(filepath.Separator)) {
		candidate = filepath.Join(candidate, segment)
		info, err := os.Lstat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				break
			}
			return "", filesystemError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", invalidWritePath(fmt.Sprintf("output path contains a symbolic link: %q", requested))
		}
	}
	return filepath.Join(root, cleaned), nil
}

func invalidWritePath(message string) *agentError {
	return newAgentError("INVALID_ARGUMENT", message, "Use a relative path beneath the configured write root without symbolic links.", false, nil)
}

func filesystemError(err error) *agentError {
	return newAgentError("FILESYSTEM_ERROR", err.Error(), "Check the target path and its permissions, then retry.", false, err)
}
