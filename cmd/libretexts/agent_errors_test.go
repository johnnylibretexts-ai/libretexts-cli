package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type timeoutTestError struct{}

func (timeoutTestError) Error() string { return "request timed out" }

func (timeoutTestError) Timeout() bool { return true }

func TestExtractGlobalFlagsAcceptsJSONErrorsAnywhere(t *testing.T) {
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"--json-errors", "page", "123"}, []string{"page", "123"}},
		{[]string{"page", "123", "--json-errors"}, []string{"page", "123"}},
	}
	for _, tt := range tests {
		got, opts := extractGlobalFlags(tt.args)
		if !opts.JSONErrors || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("extractGlobalFlags(%v) = %v, %+v; want %v with JSON errors", tt.args, got, opts, tt.want)
		}
	}
}

func TestWriteCLIErrorJSON(t *testing.T) {
	var out bytes.Buffer
	err := newAgentError("LIBRARY_REQUIRED", "--library is required with numeric page IDs", "Retry with --library followed by a LibreTexts library name, such as chem.", false, nil)
	if writeErr := writeCLIError(&out, err, true); writeErr != nil {
		t.Fatal(writeErr)
	}
	var got errorEnvelope
	if decodeErr := json.Unmarshal(out.Bytes(), &got); decodeErr != nil {
		t.Fatalf("invalid JSON: %v", decodeErr)
	}
	if got.SchemaVersion != "1" || got.OK || got.Error.Code != "LIBRARY_REQUIRED" || got.Error.Retryable {
		t.Fatalf("unexpected envelope: %+v", got)
	}
}

func TestWriteCLIErrorPlainPreservesLegacyPrefix(t *testing.T) {
	var out bytes.Buffer
	if err := writeCLIError(&out, errors.New("boom"), false); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "error: boom\n"; got != want {
		t.Fatalf("plain error = %q, want %q", got, want)
	}
}

func TestExecuteRendersMalformedFlagAsJSON(t *testing.T) {
	var stderr bytes.Buffer
	if got := execute(context.Background(), []string{"page", "123", "--not-a-flag", "--json-errors"}, &stderr); got != 1 {
		t.Fatalf("execute() exit code = %d, want 1", got)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("stderr is not one JSON object: %q (%v)", stderr.String(), err)
	}
	if envelope.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error code = %q, want INVALID_ARGUMENT", envelope.Error.Code)
	}
}

func TestExecuteNegativeSearchLimitReturnsOneJSONErrorWithoutRequestOrPanic(t *testing.T) {
	originalTransport := http.DefaultTransport
	requestCount := 0
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"numResults":1,"results":[{"bookID":"chem-123","title":"Test Book","library":"chem","links":{}}]}`)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	var stderr bytes.Buffer
	exitCode, panicValue := executeCapturingPanic(context.Background(), []string{"--json-errors", "search", "query", "--limit", "-1"}, &stderr)
	if panicValue != nil {
		t.Fatalf("execute panicked for a negative search limit: %v", panicValue)
	}
	if exitCode != 1 {
		t.Fatalf("execute exit code = %d, want 1", exitCode)
	}
	if requestCount != 0 {
		t.Fatalf("request count = %d, want 0", requestCount)
	}

	decoder := json.NewDecoder(bytes.NewReader(stderr.Bytes()))
	var envelope errorEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("stderr is not an error object: %q (%v)", stderr.String(), err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("stderr contains more than one JSON object: %q", stderr.String())
	}
	if envelope.SchemaVersion != "1" || envelope.OK || envelope.Error.Code != "INVALID_ARGUMENT" || envelope.Error.Message != "limit must be non-negative, got -1" || envelope.Error.Hint != "Provide a limit of zero or greater." || envelope.Error.Retryable {
		t.Fatalf("error envelope = %+v", envelope)
	}
}

func executeCapturingPanic(ctx context.Context, args []string, stderr io.Writer) (exitCode int, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	exitCode = execute(ctx, args, stderr)
	return exitCode, nil
}

func TestExtractJSONErrorsSuppressesProgressBeforeFailure(t *testing.T) {
	var requestCount int
	c := &client{http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		status := http.StatusOK
		body := `{"@id":"123","title":"Test Book","uri.ui":"https://chem.libretexts.org/test-book","subpages":{"@href":"https://chem.libretexts.org/@api/deki/pages/123/subpages"}}`
		switch {
		case strings.HasSuffix(req.URL.Path, "/tree"):
			body = `{"page":{"@id":"123","title":"Test Book","uri.ui":"https://chem.libretexts.org/test-book","subpages":{"page":{"@id":"124","title":"Chapter One","uri.ui":"https://chem.libretexts.org/test-book/chapter-one","subpages":""}}}}`
		case strings.HasSuffix(req.URL.Path, "/contents") && requestCount == 3:
			body = `{"@revision":"1","@type":"text/html","@title":"Test Book","body":"<p>Chapter text</p>"}`
		case strings.HasSuffix(req.URL.Path, "/contents"):
			status = http.StatusServiceUnavailable
			body = "unavailable"
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}

	var progress bytes.Buffer
	err := cmdExtractWithOptions(context.Background(), c, []string{"123", "--library", "chem", "--out", filepath.Join(t.TempDir(), "book"), "--delay", "0"}, globalOptions{JSONErrors: true}, &progress)
	if err == nil {
		t.Fatal("cmdExtractWithOptions() error = nil, want upstream failure")
	}
	if got := progress.String(); got != "" {
		t.Fatalf("JSON-error extract progress = %q, want empty", got)
	}
}

func TestClassifyErrorBranches(t *testing.T) {
	coded := newAgentError("KNOWN", "known", "", false, nil)
	if got := classifyError(coded); got != coded {
		t.Fatalf("classified agent error = %#v, want original", got)
	}
	if got := classifyError(&url.Error{Op: "Get", URL: "https://example.test", Err: errors.New("offline")}); got.Code != "NETWORK_ERROR" || !got.Retryable {
		t.Fatalf("URL error classification = %+v", got)
	}
	if got := classifyError(timeoutTestError{}); got.Code != "NETWORK_ERROR" || !got.Retryable {
		t.Fatalf("timeout classification = %+v", got)
	}
	if got := classifyError(&os.PathError{Op: "open", Path: "missing", Err: os.ErrNotExist}); got.Code != "FILESYSTEM_ERROR" || got.Retryable {
		t.Fatalf("path error classification = %+v", got)
	}
	if got := classifyError(errors.New("unexpected")); got.Code != "INTERNAL_ERROR" || got.Retryable {
		t.Fatalf("fallback classification = %+v", got)
	}
}

func TestUpstreamHTTPErrorRetryability(t *testing.T) {
	for _, tt := range []struct {
		status    int
		retryable bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusTooManyRequests, true},
		{http.StatusServiceUnavailable, true},
	} {
		if got := upstreamHTTPError("HTTP status", tt.status); got.Code != "UPSTREAM_HTTP_ERROR" || got.Retryable != tt.retryable {
			t.Fatalf("status %d = %+v, want retryable %t", tt.status, got, tt.retryable)
		}
	}
}
