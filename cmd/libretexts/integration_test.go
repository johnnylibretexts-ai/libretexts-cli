//go:build integration

// Integration tests run against the live LibreTexts services. They exist so a
// contract that has moved or gone dead upstream fails the build instead of
// shipping. Run them with:
//
//	go test ./... -tags=integration -count=1
//
// The unit suite does not build this file, so day-to-day `go test ./...` stays
// offline and fast.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A stable, long-published book used as the fixture throughout.
const (
	liveLibrary = "chem"
	liveBookID  = "21927"
	liveBookRef = "chem-21927"
	liveBookURL = "https://chem.libretexts.org/Bookshelves/Organic_Chemistry/Basic_Principles_of_Organic_Chemistry_(Roberts_and_Caserio)"
)

func liveContext(t *testing.T) (context.Context, *client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx, newClient()
}

func TestLiveSearchBooks(t *testing.T) {
	ctx, c := liveContext(t)
	books, err := c.searchBooks(ctx, "organic chemistry", liveLibrary, 3)
	if err != nil {
		t.Fatalf("Commons search failed: %v", err)
	}
	if len(books) == 0 {
		t.Fatal("Commons search returned no results")
	}
	for _, book := range books {
		if book.BookID == "" || book.Title == "" || book.Links.Online == "" {
			t.Fatalf("Commons result is missing core fields: %+v", book)
		}
	}
}

func TestLivePageMetadataHasFullRecord(t *testing.T) {
	ctx, c := liveContext(t)
	meta, err := c.pageMeta(ctx, liveLibrary, liveBookID)
	if err != nil {
		t.Fatalf("Deki page metadata failed: %v", err)
	}
	if meta.Title == "" || meta.URI == "" {
		t.Fatalf("metadata is missing core fields: %+v", meta)
	}
	if meta.Modified == "" {
		t.Errorf("metadata has no modified date; the Deki response shape may have changed: %+v", meta)
	}
}

func TestLivePageContent(t *testing.T) {
	ctx, c := liveContext(t)
	page, err := getPage(ctx, c, pageRequest{Identifier: liveBookRef, Content: "text", MaxChars: 500})
	if err != nil {
		t.Fatalf("page retrieval failed: %v", err)
	}
	if strings.TrimSpace(page.Content.Text) == "" {
		t.Fatal("page returned empty text")
	}
	if page.Window == nil {
		t.Fatal("bounded request returned no content window")
	}
	if page.Window.TotalChars <= 0 || page.Window.ReturnedChars > 500 {
		t.Fatalf("content window is inconsistent: %+v", page.Window)
	}
}

func TestLiveTreeTraversal(t *testing.T) {
	ctx, c := liveContext(t)
	pages, err := getTree(ctx, c, liveBookRef, "", 10)
	if err != nil {
		t.Fatalf("tree traversal failed: %v", err)
	}
	if len(pages) < 2 {
		t.Fatalf("tree returned %d pages, want the root plus children", len(pages))
	}
	if pages[0].Depth != 0 {
		t.Fatalf("first tree entry is not the root: %+v", pages[0])
	}
	var sawChild bool
	for _, page := range pages {
		if page.Depth > 0 {
			sawChild = true
		}
		if page.Meta.ID == "" || page.Meta.Title == "" {
			t.Fatalf("tree entry is missing core fields: %+v", page.Meta)
		}
	}
	if !sawChild {
		t.Fatal("tree returned no child pages; subpage traversal may be broken")
	}
}

// The regression that motivated the PDF rewrite: the Deki contents.alt URL that
// page metadata advertises returns 500, so the download service is the endpoint
// that has to keep working.
func TestLivePDFEndpointServesBooks(t *testing.T) {
	ctx, c := liveContext(t)
	meta, err := c.pageMeta(ctx, liveLibrary, liveBookID)
	if err != nil {
		t.Fatalf("page metadata failed: %v", err)
	}

	resp, err := c.fetchPDF(ctx, meta)
	if err != nil {
		t.Fatalf("no endpoint served a PDF for %s: %v", liveBookRef, err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "application/pdf") {
		t.Errorf("PDF response Content-Type = %q, want application/pdf", got)
	}
	prefix := make([]byte, 5)
	if _, err := resp.Body.Read(prefix); err != nil {
		t.Fatalf("reading PDF body: %v", err)
	}
	if string(prefix) != "%PDF-" {
		t.Fatalf("response is not a PDF, body starts with %q", prefix)
	}
}

// Pages with no rendered PDF must report PDF_UNAVAILABLE rather than a
// misleading upstream error. This resolves the page first, exactly as the pdf
// command does, so meta.PDF is populated and the Deki fallback is exercised.
// An earlier version constructed pageMeta directly, skipped the fallback, and
// missed a bug where the fallback's 500 masked the authoritative 404.
func TestLivePDFUnavailableForLeafPage(t *testing.T) {
	ctx, c := liveContext(t)
	meta, err := c.resolvePage(ctx, "chem-189162", "")
	if err != nil {
		t.Fatalf("resolving the leaf page failed: %v", err)
	}
	if meta.PDF == "" {
		t.Fatal("fixture no longer advertises a Deki PDF URL, so the fallback is not covered")
	}

	_, err = c.fetchPDF(ctx, meta)
	assertAgentErrorCode(t, err, "PDF_UNAVAILABLE")
	var coded *agentError
	if !errors.As(err, &coded) {
		t.Fatalf("error is not an agentError: %v", err)
	}
	if coded.Retryable {
		t.Fatal("a page with no published PDF was reported as retryable")
	}
}

// An unknown library must fail immediately rather than as a DNS error.
func TestLiveUnknownLibraryFailsFast(t *testing.T) {
	ctx, c := liveContext(t)
	_, err := c.resolvePage(ctx, "zzz-1", "")
	assertAgentErrorCode(t, err, "UNKNOWN_LIBRARY")
}

// Downloads the smallest published PDF end to end, so path handling and the
// byte count are exercised against a real response body.
func TestLiveDownloadPDFWritesFile(t *testing.T) {
	ctx, c := liveContext(t)
	out := filepath.Join(t.TempDir(), "section.pdf")
	result, err := downloadPDF(ctx, c, downloadRequest{Identifier: "chem-182332", OutputFile: out})
	if err != nil {
		t.Fatalf("PDF download failed: %v", err)
	}
	if result.Bytes <= 0 {
		t.Fatalf("download reported %d bytes", result.Bytes)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != int(result.Bytes) {
		t.Fatalf("wrote %d bytes, result reported %d", len(written), result.Bytes)
	}
	if !strings.HasPrefix(string(written), "%PDF-") {
		t.Fatalf("downloaded file is not a PDF, starts with %q", written[:min(8, len(written))])
	}
}

// Resolving by URL used to yield a thin record and then poison the cache for
// the same page ID, so a later book-ID lookup lost the PDF URL and modified
// date. Both orders must now produce the full record.
func TestLiveURLAndBookIDResolveIdentically(t *testing.T) {
	ctx, c := liveContext(t)

	fromURL, err := c.resolvePage(ctx, liveBookURL, "")
	if err != nil {
		t.Fatalf("resolving by URL failed: %v", err)
	}
	fromBookID, err := c.resolvePage(ctx, liveBookRef, "")
	if err != nil {
		t.Fatalf("resolving by book ID after a URL lookup failed: %v", err)
	}

	if fromURL.ID != liveBookID || fromBookID.ID != liveBookID {
		t.Fatalf("page IDs disagree: url=%q bookID=%q", fromURL.ID, fromBookID.ID)
	}
	for _, tc := range []struct {
		route string
		meta  pageMeta
	}{{"URL", fromURL}, {"book ID", fromBookID}} {
		if tc.meta.Modified == "" {
			t.Errorf("resolving by %s produced a record with no modified date: %+v", tc.route, tc.meta)
		}
		if strings.Contains(tc.meta.Title, "LibreTexts") {
			t.Errorf("resolving by %s produced a scraped document title: %q", tc.route, tc.meta.Title)
		}
	}
	if fromURL.Title != fromBookID.Title {
		t.Errorf("titles disagree: url=%q bookID=%q", fromURL.Title, fromBookID.Title)
	}
}

// Guards the host allowlist against a redirect or rename that would silently
// send requests somewhere unexpected.
func TestLiveLibraryHostsResolve(t *testing.T) {
	ctx, c := liveContext(t)
	for _, library := range libraries {
		resp, err := c.get(ctx, "https://"+library+".libretexts.org/@api/deki/site/query?dream.out.format=json&limit=1")
		if err != nil {
			t.Errorf("%s.libretexts.org is unreachable: %v", library, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("%s.libretexts.org returned HTTP %d", library, resp.StatusCode)
		}
	}
}
