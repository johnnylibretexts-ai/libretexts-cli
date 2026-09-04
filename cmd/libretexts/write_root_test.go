package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWriteRootCreatesAbsoluteDirectory(t *testing.T) {
	requested := filepath.Join(t.TempDir(), "new", "root")
	root, err := resolveWriteRoot(requested)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("resolved root = %q, want absolute path", root)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("resolved root stat = %+v, %v", info, err)
	}
}

func TestResolveWriteRootResolvesRootSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	root, err := resolveWriteRoot(link)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(rootInfo, targetInfo) || root == link {
		t.Fatalf("resolved root = %q, want target represented by %q", root, target)
	}
}

func TestConfinedOutputPathRejectsEscapes(t *testing.T) {
	root, err := resolveWriteRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"", "../outside", "nested/../../outside", "/tmp/outside"} {
		_, err := confinedOutputPath(root, requested)
		assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
	}
}

func TestConfinedOutputPathRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	root, err := resolveWriteRoot(filepath.Join(base, "root"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	_, err = confinedOutputPath(root, "link/file.pdf")
	assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
}

func TestConfinedOutputPathRejectsExistingFinalFileSymlink(t *testing.T) {
	base := t.TempDir()
	root, err := resolveWriteRoot(filepath.Join(base, "root"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.pdf")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "book.pdf")); err != nil {
		t.Fatal(err)
	}
	_, err = confinedOutputPath(root, "book.pdf")
	assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
	if got, readErr := os.ReadFile(outside); readErr != nil || string(got) != "unchanged" {
		t.Fatalf("outside target = %q, err = %v", got, readErr)
	}
}

func TestResolveWriteRootReturnsTypedFilesystemError(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveWriteRoot(filepath.Join(file, "child"))
	var coded *agentError
	if !errors.As(err, &coded) || coded.Code != "FILESYSTEM_ERROR" {
		t.Fatalf("error = %v, want FILESYSTEM_ERROR", err)
	}
}
