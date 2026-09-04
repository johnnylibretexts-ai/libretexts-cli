package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingWriteCloser struct {
	writeErr error
	closeErr error
	closed   bool
}

type capturedRead struct {
	data []byte
	err  error
}

func captureProcessOutput(t *testing.T, run func() error) ([]byte, []byte, error) {
	t.Helper()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		t.Fatal(err)
	}

	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	t.Cleanup(func() {
		os.Stdout, os.Stderr = originalStdout, originalStderr
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		_ = stdoutReader.Close()
		_ = stderrReader.Close()
	})

	stdoutResult := make(chan capturedRead, 1)
	stderrResult := make(chan capturedRead, 1)
	go func() {
		data, readErr := io.ReadAll(stdoutReader)
		stdoutResult <- capturedRead{data: data, err: readErr}
	}()
	go func() {
		data, readErr := io.ReadAll(stderrReader)
		stderrResult <- capturedRead{data: data, err: readErr}
	}()

	runErr := run()
	os.Stdout, os.Stderr = originalStdout, originalStderr
	stdoutCloseErr := stdoutWriter.Close()
	stderrCloseErr := stderrWriter.Close()
	stdout := <-stdoutResult
	stderr := <-stderrResult
	if stdout.err != nil {
		t.Fatalf("read captured stdout: %v", stdout.err)
	}
	if stderr.err != nil {
		t.Fatalf("read captured stderr: %v", stderr.err)
	}
	if stdoutCloseErr != nil {
		t.Fatalf("close captured stdout: %v", stdoutCloseErr)
	}
	if stderrCloseErr != nil {
		t.Fatalf("close captured stderr: %v", stderrCloseErr)
	}
	return stdout.data, stderr.data, runErr
}

func (w *failingWriteCloser) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}

func (w *failingWriteCloser) Close() error {
	w.closed = true
	return w.closeErr
}

func TestExtractBookReturnsResultWithoutProgress(t *testing.T) {
	c, _ := newTestClient(t)
	out := filepath.Join(t.TempDir(), "book")
	result, err := extractBook(context.Background(), c, extractRequest{
		Identifier: "123", Library: "chem", OutputDir: out,
		Format: "markdown", MaxPages: 1, Progress: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PageCount != 1 || result.IndexPath != filepath.Join(out, "index.json") {
		t.Fatalf("extract result = %+v", result)
	}
}

func TestExtractBookWithNilProgressDoesNotWriteProcessStreams(t *testing.T) {
	c, _ := newTestClient(t)
	out := filepath.Join(t.TempDir(), "book")
	stdout, stderr, err := captureProcessOutput(t, func() error {
		_, extractErr := extractBook(context.Background(), c, extractRequest{
			Identifier: "123", Library: "chem", OutputDir: out,
			Format: "markdown", MaxPages: 1, Progress: nil,
		})
		return extractErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("process output: stdout=%q stderr=%q, want both empty", stdout, stderr)
	}
}

func TestExtractBookDelayStopsOnContextCancellation(t *testing.T) {
	c, _ := newTestClient(t)
	outputDir := filepath.Join(t.TempDir(), "book")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := extractBook(ctx, c, extractRequest{
			Identifier: "123", Library: "chem", OutputDir: outputDir,
			Format: "markdown", MaxPages: 1, Delay: 2 * time.Second,
		})
		done <- err
	}()

	pagePath := filepath.Join(outputDir, "0001-test-book.md")
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		if _, err := os.Stat(pagePath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("page was not written before cancellation deadline")
		case <-ticker.C:
		}
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("extractBook error = %v, want context.Canceled", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("extractBook did not stop promptly after context cancellation")
	}
}

func TestDownloadPDFReturnsPathAndByteCount(t *testing.T) {
	c, _ := newTestClient(t)
	out := filepath.Join(t.TempDir(), "book.pdf")
	result, err := downloadPDF(context.Background(), c, downloadRequest{
		Identifier: "123", Library: "chem", OutputFile: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputFile != out || result.Bytes != int64(len("%PDF-1.4 test")) {
		t.Fatalf("download result = %+v", result)
	}
}

func TestWriteJSONAndCloseReturnsCloseError(t *testing.T) {
	closeErr := &os.PathError{Op: "close", Path: "result.json", Err: errors.New("close failed")}
	w := &failingWriteCloser{closeErr: closeErr}
	err := writeJSONAndClose(w, map[string]string{"ok": "yes"})
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want close error", err)
	}
	assertAgentErrorCode(t, classifyError(err), "FILESYSTEM_ERROR")
	if !w.closed {
		t.Fatal("writer was not closed")
	}
}

func TestWriteJSONAndClosePreservesEncodeError(t *testing.T) {
	writeErr := errors.New("write failed")
	w := &failingWriteCloser{writeErr: writeErr, closeErr: errors.New("close failed")}
	if err := writeJSONAndClose(w, map[string]string{"ok": "yes"}); !errors.Is(err, writeErr) {
		t.Fatalf("error = %v, want write error", err)
	}
	if !w.closed {
		t.Fatal("writer was not closed")
	}
}

func TestCopyAndCloseReturnsCloseErrorAfterSuccessfulCopy(t *testing.T) {
	closeErr := &os.PathError{Op: "close", Path: "book.pdf", Err: errors.New("close failed")}
	w := &failingWriteCloser{closeErr: closeErr}
	written, err := copyAndClose(w, bytes.NewBufferString("pdf"))
	if written != 3 || !errors.Is(err, closeErr) {
		t.Fatalf("copy result = (%d, %v), want (3, close error)", written, err)
	}
	assertAgentErrorCode(t, classifyError(err), "FILESYSTEM_ERROR")
}

func TestCopyAndClosePreservesCopyError(t *testing.T) {
	copyErr := errors.New("copy failed")
	w := &failingWriteCloser{writeErr: copyErr, closeErr: errors.New("close failed")}
	_, err := copyAndClose(w, io.LimitReader(strings.NewReader("pdf"), 3))
	if !errors.Is(err, copyErr) {
		t.Fatalf("error = %v, want copy error", err)
	}
	if !w.closed {
		t.Fatal("writer was not closed")
	}
}

func assertAgentErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var coded *agentError
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func TestGetPageMetadataSkipsContentsRequest(t *testing.T) {
	c, requests := newTestClient(t)
	result, err := getPage(context.Background(), c, pageRequest{Identifier: "123", Library: "chem", Content: "metadata"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 1 || result.Meta.ID != "123" || result.Content.HTML != "" {
		t.Fatalf("metadata result=%+v requests=%d", result, len(*requests))
	}
}

func TestGetPageBothStillReturnsTextAndHTML(t *testing.T) {
	c, requests := newTestClient(t)
	result, err := getPage(context.Background(), c, pageRequest{Identifier: "123", Library: "chem", Content: "both"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content.Text != "Chapter text" || result.Content.HTML != "<p>Chapter text</p>" {
		t.Fatalf("both content = %+v", result.Content)
	}
	if len(*requests) != 2 {
		t.Fatalf("request count = %d, want metadata plus contents", len(*requests))
	}
}

func TestGetPageReturnsBoundedText(t *testing.T) {
	c, _ := newTestClient(t)
	result, err := getPage(context.Background(), c, pageRequest{Identifier: "123", Library: "chem", Content: "text", MaxChars: 7})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content.Text != "Chapter" || result.Window == nil || result.Window.NextOffset == nil {
		t.Fatalf("bounded page = %+v", result)
	}
}

func TestGetPageWindowJSONShape(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		maxChars   int
		wantWindow bool
	}{
		{"unbounded text", "text", 0, false},
		{"unbounded html", "html", 0, false},
		{"bounded text", "text", 7, true},
		{"bounded html", "html", 7, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestClient(t)
			result, err := getPage(context.Background(), c, pageRequest{Identifier: "123", Library: "chem", Content: tt.content, MaxChars: tt.maxChars})
			if err != nil {
				t.Fatal(err)
			}
			if (result.Window != nil) != tt.wantWindow {
				t.Fatalf("window = %+v, want present=%t", result.Window, tt.wantWindow)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(encoded), `"window"`); got != tt.wantWindow {
				t.Fatalf("JSON = %s, window present=%t, want %t", encoded, got, tt.wantWindow)
			}
		})
	}
}

func TestGetTreeUsesWalkResults(t *testing.T) {
	c, _ := newTestClient(t)
	results, err := getTree(context.Background(), c, "123", "chem", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Meta.ID != "123" || results[0].Depth != 0 {
		t.Fatalf("tree results = %+v", results)
	}
}

func TestPageLegacyRequestsMetadataAndContents(t *testing.T) {
	c, requests := newTestClient(t)
	if err := cmdPage(context.Background(), c, []string{"123", "--library", "chem", "--json"}); err != nil {
		t.Fatal(err)
	}
	if len(*requests) != 2 {
		t.Fatalf("request count = %d, want metadata plus contents", len(*requests))
	}
}

func TestPageRejectsBoundsWithBoth(t *testing.T) {
	c, _ := newTestClient(t)
	err := cmdPage(context.Background(), c, []string{"123", "--library", "chem", "--content", "both", "--max-chars", "7"})
	var coded *agentError
	if !errors.As(err, &coded) || coded.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestPageRejectsExplicitEmptyContentMode(t *testing.T) {
	c, _ := newTestClient(t)
	err := cmdPage(context.Background(), c, []string{"123", "--library", "chem", "--content", ""})
	var coded *agentError
	if !errors.As(err, &coded) || coded.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}
