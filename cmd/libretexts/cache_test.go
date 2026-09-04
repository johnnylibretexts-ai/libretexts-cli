package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// stubClient serves a fixed body per request without recording requests, so it
// is safe to drive from several goroutines at once.
func stubClient(handler func(*http.Request) (int, string)) *client {
	return &client{http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status, body := handler(req)
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}}
}

func TestCachedMetaIsScopedByLibrary(t *testing.T) {
	c := stubClient(func(req *http.Request) (int, string) {
		library := strings.SplitN(req.URL.Hostname(), ".", 2)[0]
		return http.StatusOK, `{"@id":"12345","title":"` + library + ` page","uri.ui":"https://` + library + `.libretexts.org/p"}`
	})

	chem, err := c.pageMeta(context.Background(), "chem", "12345")
	if err != nil {
		t.Fatal(err)
	}
	bio, err := c.pageMeta(context.Background(), "bio", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if chem.Title != "chem page" {
		t.Fatalf("chem title = %q, want %q", chem.Title, "chem page")
	}
	if bio.Title != "bio page" {
		t.Fatalf("bio/12345 served the cached chem record: title = %q", bio.Title)
	}
}

func TestCachedContentIsScopedByLibrary(t *testing.T) {
	c := stubClient(func(req *http.Request) (int, string) {
		library := strings.SplitN(req.URL.Hostname(), ".", 2)[0]
		return http.StatusOK, `{"body":"<p>` + library + ` body</p>"}`
	})

	if _, err := c.pageContent(context.Background(), "chem", "12345"); err != nil {
		t.Fatal(err)
	}
	bio, err := c.pageContent(context.Background(), "bio", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bio.HTML, "bio body") {
		t.Fatalf("bio/12345 served the cached chem body: %q", bio.HTML)
	}
}

// Scraped metadata omits the PDF URL and modified date, so it must not satisfy
// a later lookup that the API can answer in full.
func TestPartialMetaDoesNotSatisfyLookup(t *testing.T) {
	c := stubClient(func(req *http.Request) (int, string) {
		return http.StatusOK, `{"@id":"12345","title":"Full Record","uri.ui":"https://chem.libretexts.org/p","date.modified":"Tue, 07 Mar 2023 23:48:38 GMT","contents.alt":{"@href":"https://chem.libretexts.org/p.pdf"}}`
	})
	c.storeMeta("chem", "12345", pageMeta{ID: "12345", Title: "Scraped - Chemistry LibreTexts", Library: "chem"}, true)

	meta, err := c.pageMeta(context.Background(), "chem", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Full Record" {
		t.Fatalf("title = %q, want the API record", meta.Title)
	}
	if meta.PDF == "" || meta.Modified == "" {
		t.Fatalf("API record was not used: pdf=%q modified=%q", meta.PDF, meta.Modified)
	}
}

// A partial record is still better than nothing when the API is unreachable.
func TestPartialMetaServesAsFallbackWhenAPIFails(t *testing.T) {
	c := stubClient(func(req *http.Request) (int, string) {
		return http.StatusInternalServerError, `{}`
	})
	c.storeMeta("chem", "12345", pageMeta{ID: "12345", Title: "Scraped", Library: "chem"}, true)

	meta, err := c.pageMeta(context.Background(), "chem", "12345")
	if err != nil {
		t.Fatalf("expected the cached record to cover the failure, got %v", err)
	}
	if meta.Title != "Scraped" {
		t.Fatalf("title = %q, want the cached record", meta.Title)
	}
}

// Nothing drives the client concurrently today, but the caches are shared for
// the life of a process and a parallel walk would be a natural addition. Run
// with -race.
func TestCacheIsSafeForConcurrentUse(t *testing.T) {
	c := stubClient(func(req *http.Request) (int, string) {
		return http.StatusOK, `{"@id":"12345","title":"Test","uri.ui":"https://chem.libretexts.org/p","body":"<p>x</p>"}`
	})

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			library := []string{"chem", "bio", "math"}[i%3]
			if _, err := c.pageMeta(context.Background(), library, "12345"); err != nil {
				t.Error(err)
			}
			if _, err := c.pageContent(context.Background(), library, "12345"); err != nil {
				t.Error(err)
			}
			c.storeMeta(library, "999", pageMeta{ID: "999"}, true)
			c.storeContent(library, "999", pageContent{HTML: "<p>x</p>"})
		}(i)
	}
	wg.Wait()
}

func TestFetchPDFPrefersDownloadServiceAndFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name             string
		downloadStatus   int
		dekiPDF          string
		wantBody         string
		wantErrCode      string
		wantDownloadHits int
	}{
		{name: "download service serves the PDF", downloadStatus: http.StatusOK, dekiPDF: "https://chem.libretexts.org/p.pdf", wantBody: "%PDF download", wantDownloadHits: 1},
		{name: "falls back to the deki URL on 404", downloadStatus: http.StatusNotFound, dekiPDF: "https://chem.libretexts.org/p.pdf", wantBody: "%PDF deki", wantDownloadHits: 1},
		{name: "reports PDF_UNAVAILABLE when neither serves", downloadStatus: http.StatusNotFound, dekiPDF: "", wantErrCode: "PDF_UNAVAILABLE", wantDownloadHits: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			downloadHits := 0
			c := stubClient(func(req *http.Request) (int, string) {
				if strings.HasPrefix(req.URL.Path, "/api/v1/download/") {
					downloadHits++
					return tc.downloadStatus, "%PDF download"
				}
				return http.StatusOK, "%PDF deki"
			})

			resp, err := c.fetchPDF(context.Background(), pageMeta{ID: "12345", Library: "chem", PDF: tc.dekiPDF})
			if tc.wantErrCode != "" {
				assertAgentErrorCode(t, err, tc.wantErrCode)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(body) != tc.wantBody {
					t.Fatalf("body = %q, want %q", body, tc.wantBody)
				}
			}
			if downloadHits != tc.wantDownloadHits {
				t.Fatalf("download service hits = %d, want %d", downloadHits, tc.wantDownloadHits)
			}
		})
	}
}

func TestBookDownloadURL(t *testing.T) {
	if got, want := bookDownloadURL("chem", "21927"), "https://downloads.libretexts.org/api/v1/download/chem-21927/pdf"; got != want {
		t.Fatalf("bookDownloadURL() = %q, want %q", got, want)
	}
}

// flag.ExitOnError used to call os.Exit(2) from inside the flag package for
// these, skipping the error envelope entirely.
func TestMalformedFlagsExitOneThroughTheErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "undefined flag", args: []string{"search", "--bogus", "foo"}},
		{name: "invalid duration", args: []string{"extract", "--delay", "nope", "chem-1", "--out", "/tmp/x"}},
		{name: "wrong type", args: []string{"tree", "--max-pages", "abc", "chem-1"}},
		{name: "undefined flag on libraries", args: []string{"libraries", "--bogus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			if code := execute(context.Background(), tc.args, &stderr); code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if !strings.HasPrefix(stderr.String(), "error: ") {
				t.Fatalf("stderr = %q, want the plain error prefix", stderr.String())
			}

			stderr.Reset()
			args := append([]string{"--json-errors"}, tc.args...)
			if code := execute(context.Background(), args, &stderr); code != 1 {
				t.Fatalf("--json-errors exit code = %d, want 1", code)
			}
			var envelope errorEnvelope
			if err := json.Unmarshal([]byte(stderr.String()), &envelope); err != nil {
				t.Fatalf("stderr is not one JSON object: %q", stderr.String())
			}
			if envelope.OK || envelope.Error.Code != "INVALID_ARGUMENT" {
				t.Fatalf("envelope = %+v", envelope)
			}
			if !strings.Contains(envelope.Error.Hint, "describe --json") {
				t.Fatalf("hint does not point anywhere useful: %q", envelope.Error.Hint)
			}
		})
	}
}
