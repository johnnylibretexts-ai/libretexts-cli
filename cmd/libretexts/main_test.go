package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(t *testing.T) (*client, *[]*http.Request) {
	t.Helper()
	requests := make([]*http.Request, 0)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Clone(req.Context()))
		body := `{"@id":"123","title":"Test Book","uri.ui":"https://chem.libretexts.org/test-book","contents.alt":{"@href":"https://chem.libretexts.org/test-book.pdf"},"subpages":{"@href":"https://chem.libretexts.org/@api/deki/pages/123/subpages"}}`
		switch {
		case strings.Contains(req.URL.Path, "/api/v1/search/books-v2"):
			body = `{"err":false,"numResults":1,"results":[{"bookID":"chem-123","title":"Test Book","author":"Test Author","library":"chem","subject":"Chemistry","license":"ccby","links":{"online":"https://chem.libretexts.org/test-book","pdf":"https://downloads.libretexts.org/api/v1/download/chem-123/pdf"}}]}`
		case strings.HasSuffix(req.URL.Path, "/tree"):
			body = `{"page":{"@id":"123","title":"Test Book","uri.ui":"https://chem.libretexts.org/test-book","subpages":{"page":{"@id":"124","title":"Chapter One","uri.ui":"https://chem.libretexts.org/test-book/chapter-one","subpages":""}}}}`
		case strings.HasSuffix(req.URL.Path, "/contents"):
			body = `{"@revision":"1","@type":"text/html","@title":"Test Book","body":"<p>Chapter text</p>"}`
		case strings.HasSuffix(req.URL.Path, "/subpages"):
			body = `{"@totalcount":"0","@count":"0","page.subpage":null}`
		case strings.HasPrefix(req.URL.Path, "/api/v1/download/"):
			body = "%PDF-1.4 test"
		case strings.HasSuffix(req.URL.Path, "/test-book.pdf"):
			body = "%PDF-1.4 deki fallback"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	return &client{http: &http.Client{Transport: transport}}, &requests
}

func TestSearchAcceptsFlagsAfterQuery(t *testing.T) {
	c, requests := newTestClient(t)
	err := cmdSearch(context.Background(), c, []string{"general chemistry", "--library", "chem", "--limit", "1", "--json"})
	if err != nil {
		t.Fatalf("cmdSearch() error = %v", err)
	}
	if got := len(*requests); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if got := (*requests)[0].URL.Host; got != "commons.libretexts.org" {
		t.Fatalf("request host = %q, want %q", got, "commons.libretexts.org")
	}
	if got := (*requests)[0].URL.Path; got != "/api/v1/search/books-v2" {
		t.Fatalf("request path = %q, want Commons book search", got)
	}
	if got := (*requests)[0].URL.Query().Get("searchQuery"); got != "general chemistry" {
		t.Fatalf("query = %q, want %q", got, "general chemistry")
	}
	if got := (*requests)[0].URL.Query().Get("library"); got != "chem" {
		t.Fatalf("library = %q, want %q", got, "chem")
	}
	if got := (*requests)[0].URL.Query().Get("limit"); got != "1" {
		t.Fatalf("limit = %q, want %q", got, "1")
	}
}

func TestSearchBooksRejectsNegativeLimitBeforeRequest(t *testing.T) {
	c, requests := newTestClient(t)
	_, err, panicValue := callSearchBooksCapturingPanic(c, -1)
	if panicValue != nil {
		t.Fatalf("searchBooks panicked for a negative limit: %v", panicValue)
	}
	assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
	if got := len(*requests); got != 0 {
		t.Fatalf("request count = %d, want 0", got)
	}
}

func callSearchBooksCapturingPanic(c *client, limit int) (books []bookInfo, err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	books, err = c.searchBooks(context.Background(), "chemistry", "chem", limit)
	return books, err, nil
}

func TestResolvePageAcceptsCommonsBookID(t *testing.T) {
	c, requests := newTestClient(t)
	meta, err := c.resolvePage(context.Background(), "chem-123", "")
	if err != nil {
		t.Fatalf("resolvePage() error = %v", err)
	}
	if meta.Library != "chem" || meta.ID != "123" {
		t.Fatalf("resolved page = %s-%s, want chem-123", meta.Library, meta.ID)
	}
	if got := len(*requests); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
	if got := (*requests)[0].URL.Host; got != "chem.libretexts.org" {
		t.Fatalf("request host = %q, want %q", got, "chem.libretexts.org")
	}
}

func TestPageAcceptsFlagsAfterPageID(t *testing.T) {
	c, requests := newTestClient(t)
	err := cmdPage(context.Background(), c, []string{"123", "--library", "chem", "--json"})
	if err != nil {
		t.Fatalf("cmdPage() error = %v", err)
	}
	if got := len(*requests); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestTreeAcceptsFlagsAfterPageID(t *testing.T) {
	c, requests := newTestClient(t)
	err := cmdTree(context.Background(), c, []string{"123", "--library", "chem", "--max-pages", "1", "--json"})
	if err != nil {
		t.Fatalf("cmdTree() error = %v", err)
	}
	if got := len(*requests); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if got := (*requests)[1].URL.Path; got != "/@api/deki/pages/123/tree" {
		t.Fatalf("tree request path = %q, want /@api/deki/pages/123/tree", got)
	}
}

func TestExtractAcceptsFlagsAfterPageID(t *testing.T) {
	c, _ := newTestClient(t)
	out := filepath.Join(t.TempDir(), "book")
	err := cmdExtract(context.Background(), c, []string{"123", "--library", "chem", "--out", out, "--max-pages", "1", "--delay", "0"})
	if err != nil {
		t.Fatalf("cmdExtract() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Fatalf("index.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "0001-test-book.md")); err != nil {
		t.Fatalf("page file not written: %v", err)
	}
}

func TestPDFAcceptsFlagsAfterPageID(t *testing.T) {
	c, _ := newTestClient(t)
	out := filepath.Join(t.TempDir(), "book.pdf")
	err := cmdPDF(context.Background(), c, []string{"123", "--library", "chem", "--out", out})
	if err != nil {
		t.Fatalf("cmdPDF() error = %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read PDF: %v", err)
	}
	if got, want := string(b), "%PDF-1.4 test"; got != want {
		t.Fatalf("PDF contents = %q, want %q", got, want)
	}
}
