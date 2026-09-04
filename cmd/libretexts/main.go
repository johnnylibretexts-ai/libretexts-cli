package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const userAgent = "libretexts-cli/0.1 (+https://libretexts.org)"

var libraries = []string{
	"bio", "biz", "chem", "eng", "espanol", "geo", "human", "k12", "math", "med",
	"phys", "socialsci", "stats", "workforce",
}

const (
	requestTimeout  = 45 * time.Second
	downloadTimeout = 15 * time.Minute
)

// htmlOnlyPageID stands in for the page ID when a scraped page exposes no
// numeric identifier, so the scraped record still has a cache key.
const htmlOnlyPageID = "page"

type client struct {
	http     *http.Client
	download *http.Client

	// mu guards both caches. The MCP server shares one client across tool
	// calls, and the SDK dispatches those calls concurrently.
	mu        sync.RWMutex
	htmlCache map[string]pageContent
	metaCache map[string]cachedMeta
}

type cachedMeta struct {
	meta pageMeta
	// partial marks metadata recovered by scraping page HTML, which lacks the
	// PDF URL, path, and modified date the Deki API returns. A partial record
	// satisfies a lookup only when the API cannot be reached.
	partial bool
}

func newClient() *client {
	return &client{
		http:     &http.Client{Timeout: requestTimeout},
		download: &http.Client{Timeout: downloadTimeout},
	}
}

// cacheKey namespaces cached records by library. Page IDs are only unique
// within a library, so keying on the ID alone serves one library's page for
// another's request.
func cacheKey(library, id string) string {
	return library + "-" + id
}

func (c *client) cachedMeta(library, id string) (cachedMeta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.metaCache[cacheKey(library, id)]
	return entry, ok
}

func (c *client) storeMeta(library, id string, meta pageMeta, partial bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.metaCache == nil {
		c.metaCache = make(map[string]cachedMeta)
	}
	c.metaCache[cacheKey(library, id)] = cachedMeta{meta: meta, partial: partial}
}

func (c *client) cachedContent(library, id string) (pageContent, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	content, ok := c.htmlCache[cacheKey(library, id)]
	return content, ok
}

func (c *client) storeContent(library, id string, content pageContent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.htmlCache == nil {
		c.htmlCache = make(map[string]pageContent)
	}
	c.htmlCache[cacheKey(library, id)] = content
}

type pageMeta struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	URI      string `json:"uri"`
	Path     string `json:"path,omitempty"`
	Modified string `json:"modified,omitempty"`
	Article  string `json:"article,omitempty"`
	PDF      string `json:"pdf,omitempty"`
	HasKids  bool   `json:"has_subpages"`
	Library  string `json:"library"`
}

type pageContent struct {
	Revision string `json:"revision,omitempty"`
	Type     string `json:"type,omitempty"`
	Title    string `json:"title,omitempty"`
	HTML     string `json:"html"`
	Text     string `json:"text,omitempty"`
}

type extractedPage struct {
	Meta    pageMeta    `json:"meta"`
	Content pageContent `json:"content"`
	Depth   int         `json:"depth"`
}

type subpagesResponse struct {
	Total string          `json:"@totalcount"`
	Count string          `json:"@count"`
	Pages json.RawMessage `json:"page.subpage"`
}

type bookSearchResponse struct {
	NumResults int        `json:"numResults"`
	Results    []bookInfo `json:"results"`
}

type bookInfo struct {
	BookID      string    `json:"bookID"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	Affiliation string    `json:"affiliation,omitempty"`
	Library     string    `json:"library"`
	Subject     string    `json:"subject,omitempty"`
	License     string    `json:"license,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Thumbnail   string    `json:"thumbnail,omitempty"`
	LastUpdated string    `json:"lastUpdated,omitempty"`
	Location    string    `json:"location,omitempty"`
	Program     string    `json:"program,omitempty"`
	Links       bookLinks `json:"links"`
}

type bookLinks struct {
	Online string `json:"online,omitempty"`
	PDF    string `json:"pdf,omitempty"`
	ZIP    string `json:"zip,omitempty"`
	Files  string `json:"files,omitempty"`
	LMS    string `json:"lms,omitempty"`
}

type treeResponse struct {
	Page treePage `json:"page"`
}

type treePage struct {
	ID       string          `json:"@id"`
	Title    string          `json:"title"`
	URI      string          `json:"uri.ui"`
	Modified string          `json:"date.modified"`
	Article  string          `json:"article"`
	Path     treePath        `json:"path"`
	Subpages json.RawMessage `json:"subpages"`
}

type treePath struct {
	Text string `json:"#text"`
}

func main() {
	os.Exit(execute(context.Background(), os.Args[1:], os.Stderr))
}

func run(ctx context.Context, args []string) error {
	return runWithOptions(ctx, args, globalOptions{})
}

func runWithOptions(ctx context.Context, args []string, options globalOptions) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	c := newClient()
	switch args[0] {
	case "libraries":
		return cmdLibrariesWithOptions(args[1:], options)
	case "describe":
		return cmdDescribe(args[1:], os.Stdout)
	case "search":
		return cmdSearchWithOptions(ctx, c, args[1:], options)
	case "page":
		return cmdPageWithOptions(ctx, c, args[1:], options)
	case "tree":
		return cmdTreeWithOptions(ctx, c, args[1:], options)
	case "extract":
		return cmdExtractWithOptions(ctx, c, args[1:], options, os.Stderr)
	case "pdf":
		return cmdPDFWithOptions(ctx, c, args[1:], options)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return newAgentError("UNKNOWN_COMMAND", fmt.Sprintf("unknown command %q", args[0]), "Run libretexts help to list supported commands.", false, nil)
	}
}

func usage() {
	_ = renderHumanCapabilities(os.Stdout, canonicalCapabilities)
}

func cmdLibraries(args []string) error {
	return cmdLibrariesWithOptions(args, globalOptions{})
}

func cmdLibrariesWithOptions(args []string, options globalOptions) error {
	fs := newCommandFlagSet("libraries", options.JSONErrors)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := parseCommandFlags(fs, args); err != nil {
		return invalidArgumentError(fs.Name(), err)
	}
	if *jsonOut {
		return writeJSON(os.Stdout, libraries)
	}
	for _, lib := range libraries {
		fmt.Printf("%-10s https://%s.libretexts.org\n", lib, lib)
	}
	return nil
}

func cmdSearch(ctx context.Context, c *client, args []string) error {
	return cmdSearchWithOptions(ctx, c, args, globalOptions{})
}

func cmdSearchWithOptions(ctx context.Context, c *client, args []string, options globalOptions) error {
	fs := newCommandFlagSet("search", options.JSONErrors)
	lib := fs.String("library", "", "LibreTexts library host, e.g. chem")
	limit := fs.Int("limit", 10, "maximum results")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := parseCommandFlags(fs, args); err != nil {
		return invalidArgumentError(fs.Name(), err)
	}
	if fs.NArg() == 0 {
		return newAgentError("MISSING_ARGUMENT", "search requires a query", "Provide a search query after the search command.", false, nil)
	}
	query := strings.Join(fs.Args(), " ")
	books, err := c.searchBooks(ctx, query, *lib, *limit)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(os.Stdout, books)
	}
	for i, book := range books {
		fmt.Printf("%2d. [%s] %s\n    %s\n    id=%s", i+1, book.Library, book.Title, book.Links.Online, book.BookID)
		if book.Author != "" {
			fmt.Printf(" author=%s", book.Author)
		}
		fmt.Println()
		if book.Summary != "" {
			fmt.Printf("    %s\n", compact(book.Summary, 220))
		}
	}
	return nil
}

func cmdPage(ctx context.Context, c *client, args []string) error {
	return cmdPageWithOptions(ctx, c, args, globalOptions{})
}

func cmdPageWithOptions(ctx context.Context, c *client, args []string, options globalOptions) error {
	fs := newCommandFlagSet("page", options.JSONErrors)
	lib := fs.String("library", "", "LibreTexts library host for numeric IDs")
	jsonOut := fs.Bool("json", false, "emit JSON")
	rawHTML := fs.Bool("html", false, "print content HTML")
	contentMode := fs.String("content", "", "select content representation")
	maxChars := fs.Int("max-chars", 0, "maximum Unicode characters returned")
	offset := fs.Int("offset", 0, "Unicode character offset for a bounded content stream")
	if err := parseCommandFlags(fs, args); err != nil {
		return invalidArgumentError(fs.Name(), err)
	}
	if fs.NArg() != 1 {
		return newAgentError("MISSING_ARGUMENT", "page requires BOOK_ID_OR_URL", "Provide one LibreTexts book ID or URL.", false, nil)
	}
	content := *contentMode
	contentSpecified := false
	fs.Visit(func(f *flag.Flag) {
		contentSpecified = contentSpecified || f.Name == "content"
	})
	if !contentSpecified {
		switch {
		case *jsonOut:
			content = "both"
		case *rawHTML:
			content = "html"
		default:
			content = "text"
		}
	}
	result, err := getPage(ctx, c, pageRequest{
		Identifier: fs.Arg(0),
		Library:    *lib,
		Content:    content,
		Offset:     *offset,
		MaxChars:   *maxChars,
	})
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(os.Stdout, result)
	}
	fmt.Printf("%s\n%s\nid=%s library=%s pdf=%s\n\n", result.Meta.Title, result.Meta.URI, result.Meta.ID, result.Meta.Library, result.Meta.PDF)
	if content == "html" {
		fmt.Println(result.Content.HTML)
	} else if content != "metadata" {
		fmt.Println(result.Content.Text)
	}
	return nil
}

func cmdTree(ctx context.Context, c *client, args []string) error {
	return cmdTreeWithOptions(ctx, c, args, globalOptions{})
}

func cmdTreeWithOptions(ctx context.Context, c *client, args []string, options globalOptions) error {
	fs := newCommandFlagSet("tree", options.JSONErrors)
	lib := fs.String("library", "", "LibreTexts library host for numeric IDs")
	maxPages := fs.Int("max-pages", 0, "stop after N pages, 0 means no limit")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := parseCommandFlags(fs, args); err != nil {
		return invalidArgumentError(fs.Name(), err)
	}
	if fs.NArg() != 1 {
		return newAgentError("MISSING_ARGUMENT", "tree requires BOOK_ID_OR_URL", "Provide one LibreTexts book ID or URL.", false, nil)
	}
	pages, err := getTree(ctx, c, fs.Arg(0), *lib, *maxPages)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(os.Stdout, pages)
	}
	for _, p := range pages {
		fmt.Printf("%s- %s (%s)\n", strings.Repeat("  ", p.Depth), p.Meta.Title, p.Meta.ID)
	}
	return nil
}

func cmdExtract(ctx context.Context, c *client, args []string) error {
	return cmdExtractWithOptions(ctx, c, args, globalOptions{}, os.Stderr)
}

func cmdExtractWithOptions(ctx context.Context, c *client, args []string, options globalOptions, progress io.Writer) error {
	fs := newCommandFlagSet("extract", options.JSONErrors)
	lib := fs.String("library", "", "LibreTexts library host for numeric IDs")
	out := fs.String("out", "", "output directory")
	format := fs.String("format", "markdown", "markdown, html, or json")
	maxPages := fs.Int("max-pages", 0, "stop after N pages, 0 means no limit")
	delay := fs.Duration("delay", 100*time.Millisecond, "delay between content requests")
	if err := parseCommandFlags(fs, args); err != nil {
		return invalidArgumentError(fs.Name(), err)
	}
	if fs.NArg() != 1 {
		return newAgentError("MISSING_ARGUMENT", "extract requires BOOK_ID_OR_URL", "Provide one LibreTexts book ID or URL.", false, nil)
	}
	if *out == "" {
		return newAgentError("MISSING_OUTPUT", "extract requires --out DIR", "Retry with --out followed by an output directory.", false, nil)
	}
	if options.JSONErrors {
		progress = nil
	}
	result, err := extractBook(ctx, c, extractRequest{
		Identifier: fs.Arg(0),
		Library:    *lib,
		OutputDir:  *out,
		Format:     *format,
		MaxPages:   *maxPages,
		Delay:      *delay,
		Progress:   progress,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Extracted %d pages to %s\n", result.PageCount, result.OutputDir)
	return nil
}

func cmdPDF(ctx context.Context, c *client, args []string) error {
	return cmdPDFWithOptions(ctx, c, args, globalOptions{})
}

func cmdPDFWithOptions(ctx context.Context, c *client, args []string, options globalOptions) error {
	fs := newCommandFlagSet("pdf", options.JSONErrors)
	lib := fs.String("library", "", "LibreTexts library host for numeric IDs")
	out := fs.String("out", "", "output PDF path")
	if err := parseCommandFlags(fs, args); err != nil {
		return invalidArgumentError(fs.Name(), err)
	}
	if fs.NArg() != 1 {
		return newAgentError("MISSING_ARGUMENT", "pdf requires BOOK_ID_OR_URL", "Provide one LibreTexts book ID or URL.", false, nil)
	}
	result, err := downloadPDF(ctx, c, downloadRequest{
		Identifier: fs.Arg(0),
		Library:    *lib,
		OutputFile: *out,
	})
	if err != nil {
		return err
	}
	fmt.Printf("Downloaded %s\n", result.OutputFile)
	return nil
}

func (c *client) searchBooks(ctx context.Context, query, library string, limit int) ([]bookInfo, error) {
	if limit < 0 {
		return nil, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("limit must be non-negative, got %d", limit), "Provide a limit of zero or greater.", false, nil)
	}
	if library != "" && library != "all" && !knownLibrary(library) {
		return nil, unknownLibraryError(library)
	}
	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("page", "1")
	params.Set("sort", "title")
	params.Set("searchQuery", query)
	if library != "" && library != "all" {
		params.Set("library", library)
	}
	u := "https://commons.libretexts.org/api/v1/search/books-v2?" + params.Encode()
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, upstreamHTTPError(fmt.Sprintf("HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	var sr bookSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	if len(sr.Results) > limit {
		sr.Results = sr.Results[:limit]
	}
	return sr.Results, nil
}

func (c *client) resolvePage(ctx context.Context, input, library string) (pageMeta, error) {
	if bookLibrary, pageID, ok := parseBookID(input); ok {
		if library != "" && library != bookLibrary {
			return pageMeta{}, newAgentError("LIBRARY_CONFLICT", fmt.Sprintf("book ID library %q conflicts with --library %q", bookLibrary, library), "Use the library encoded in the book ID or supply a matching --library value.", false, nil)
		}
		if !knownLibrary(bookLibrary) {
			return pageMeta{}, unknownLibraryError(bookLibrary)
		}
		return c.pageMeta(ctx, bookLibrary, pageID)
	}
	if _, err := strconv.Atoi(input); err == nil {
		if library == "" {
			return pageMeta{}, newAgentError("LIBRARY_REQUIRED", "--library is required with numeric page IDs", "Retry with --library followed by a LibreTexts library name, such as chem.", false, nil)
		}
		if !knownLibrary(library) {
			return pageMeta{}, unknownLibraryError(library)
		}
		return c.pageMeta(ctx, library, input)
	}
	u, err := url.Parse(input)
	if err != nil || u.Host == "" {
		return pageMeta{}, newAgentError("INVALID_IDENTIFIER", fmt.Sprintf("expected a LibreTexts URL or numeric page ID: %q", input), "Provide a LibreTexts URL, a library-prefixed book ID, or a numeric ID with --library.", false, err)
	}
	lib := strings.TrimSuffix(u.Hostname(), ".libretexts.org")
	if lib == u.Hostname() || lib == "" {
		return pageMeta{}, newAgentError("INVALID_IDENTIFIER", fmt.Sprintf("not a LibreTexts library host: %s", u.Hostname()), "Provide a URL hosted at a LibreTexts library domain.", false, nil)
	}
	if id, ok := pageIDFromURLPath(u.Path); ok {
		if m, err := c.pageMeta(ctx, lib, id); err == nil {
			return m, nil
		}
	}
	if meta, err := c.pageFromHTML(ctx, input, lib); err == nil {
		// Scraping recovers the page ID but only a thin slice of the metadata the
		// API returns, so upgrade to the API record whenever it is available.
		if meta.ID != htmlOnlyPageID {
			if full, err := c.pageMeta(ctx, lib, meta.ID); err == nil {
				return full, nil
			}
		}
		return meta, nil
	}
	id, err := c.pageIDFromHTML(ctx, input)
	if err != nil {
		return pageMeta{}, err
	}
	return c.pageMeta(ctx, lib, id)
}

func (c *client) pageMeta(ctx context.Context, library, id string) (pageMeta, error) {
	if entry, ok := c.cachedMeta(library, id); ok && !entry.partial {
		return entry.meta, nil
	}
	u := fmt.Sprintf("https://%s.libretexts.org/@api/deki/pages/%s?dream.out.format=json", library, url.PathEscape(id))
	resp, err := c.get(ctx, u)
	if err != nil {
		if entry, ok := c.cachedMeta(library, id); ok {
			return entry.meta, nil
		}
		return pageMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if entry, ok := c.cachedMeta(library, id); ok {
			return entry.meta, nil
		}
		return pageMeta{}, upstreamHTTPError(fmt.Sprintf("page metadata failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return pageMeta{}, err
	}
	meta := pageMeta{
		ID:       asString(raw["@id"]),
		Title:    asString(raw["title"]),
		URI:      asString(raw["uri.ui"]),
		Modified: firstNonEmpty(asString(raw["date.modified"]), asString(raw["date.edited"])),
		Article:  asString(raw["article"]),
		Library:  library,
	}
	if p, ok := raw["path"].(map[string]any); ok {
		meta.Path = asString(p["#text"])
	}
	if sub, ok := raw["subpages"].(map[string]any); ok && asString(sub["@href"]) != "" {
		meta.HasKids = true
	}
	if alt, ok := raw["contents.alt"].(map[string]any); ok {
		meta.PDF = asString(alt["@href"])
	}
	c.storeMeta(library, id, meta, false)
	return meta, nil
}

func (c *client) pageContent(ctx context.Context, library, id string) (pageContent, error) {
	if content, ok := c.cachedContent(library, id); ok {
		return content, nil
	}
	u := fmt.Sprintf("https://%s.libretexts.org/@api/deki/pages/%s/contents?dream.out.format=json", library, url.PathEscape(id))
	resp, err := c.get(ctx, u)
	if err != nil {
		if content, ok := c.cachedContent(library, id); ok {
			return content, nil
		}
		return pageContent{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if content, ok := c.cachedContent(library, id); ok {
			return content, nil
		}
		return pageContent{}, upstreamHTTPError(fmt.Sprintf("page content failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return pageContent{}, err
	}
	return pageContent{
		Revision: asString(raw["@revision"]),
		Type:     asString(raw["@type"]),
		Title:    asString(raw["@title"]),
		HTML:     contentBodyHTML(raw["body"]),
	}, nil
}

func (c *client) pageFromHTML(ctx context.Context, pageURL, lib string) (pageMeta, error) {
	resp, err := c.get(ctx, pageURL)
	if err != nil {
		return pageMeta{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return pageMeta{}, upstreamHTTPError(fmt.Sprintf("HTML page fetch failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return pageMeta{}, err
	}
	htmlStr := string(b)
	reID := regexp.MustCompile(`<li[^>]+id=["']pageNumberHolder["'][\s\S]*?<dd>(\d+)</dd>`)
	mID := reID.FindStringSubmatch(htmlStr)
	id := ""
	if len(mID) >= 2 {
		id = mID[1]
	}
	reTitle := regexp.MustCompile(`<h1[^>]*id=["']title["'][^>]*>(.*?)</h1>`)
	mTitle := reTitle.FindStringSubmatch(htmlStr)
	title := ""
	if len(mTitle) >= 2 {
		title = strings.TrimSpace(htmlText(mTitle[1]))
	}
	if title == "" {
		reDocTitle := regexp.MustCompile(`<title>(.*?)</title>`)
		mDocTitle := reDocTitle.FindStringSubmatch(htmlStr)
		if len(mDocTitle) >= 2 {
			title = strings.TrimSpace(htmlText(mDocTitle[1]))
		}
	}
	contentHTML := extractSectionHTML(htmlStr)
	if id == "" && contentHTML == "" {
		return pageMeta{}, newAgentError("INVALID_IDENTIFIER", "could not extract LibreTexts page content from HTML", "Provide a valid LibreTexts URL.", false, nil)
	}
	if id == "" {
		id = htmlOnlyPageID
	}
	meta := pageMeta{
		ID:      id,
		Title:   title,
		URI:     pageURL,
		Library: lib,
	}
	content := pageContent{
		Title: title,
		HTML:  contentHTML,
		Text:  htmlText(contentHTML),
	}
	c.storeMeta(lib, id, meta, true)
	c.storeContent(lib, id, content)
	return meta, nil
}

func extractSectionHTML(html string) string {
	re := regexp.MustCompile(`(?i)<section[^>]*class=["'][^"']*mt-content-container[^"']*["'][^>]*>`)
	loc := re.FindStringIndex(html)
	if loc == nil {
		return ""
	}
	start := loc[1]
	depth := 1
	pos := start
	tagRe := regexp.MustCompile(`(?i)</?section\b[^>]*>`)
	for depth > 0 && pos < len(html) {
		subLoc := tagRe.FindStringIndex(html[pos:])
		if subLoc == nil {
			break
		}
		tagStart := pos + subLoc[0]
		tagEnd := pos + subLoc[1]
		tag := strings.ToLower(html[tagStart:tagEnd])
		if strings.HasPrefix(tag, "</") {
			depth--
			if depth == 0 {
				return strings.TrimSpace(html[start:tagStart])
			}
		} else if !strings.HasSuffix(tag, "/>") {
			depth++
		}
		pos = tagEnd
	}
	return strings.TrimSpace(html[start:])
}

func (c *client) subpages(ctx context.Context, meta pageMeta) ([]pageMeta, error) {
	u := fmt.Sprintf("https://%s.libretexts.org/@api/deki/pages/%s/subpages?limit=all&dream.out.format=json", meta.Library, url.PathEscape(meta.ID))
	resp, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, upstreamHTTPError(fmt.Sprintf("subpages failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	var sr subpagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	var raws []map[string]any
	if len(sr.Pages) == 0 || string(sr.Pages) == "null" {
		return nil, nil
	}
	if sr.Pages[0] == '[' {
		if err := json.Unmarshal(sr.Pages, &raws); err != nil {
			return nil, err
		}
	} else {
		var one map[string]any
		if err := json.Unmarshal(sr.Pages, &one); err != nil {
			return nil, err
		}
		raws = append(raws, one)
	}
	out := make([]pageMeta, 0, len(raws))
	for _, raw := range raws {
		child := pageMeta{
			ID:       asString(raw["@id"]),
			Title:    asString(raw["title"]),
			URI:      asString(raw["uri.ui"]),
			Modified: asString(raw["date.modified"]),
			Article:  asString(raw["article"]),
			Library:  meta.Library,
			HasKids:  asString(raw["@subpages"]) == "true",
		}
		if p, ok := raw["path"].(map[string]any); ok {
			child.Path = asString(p["#text"])
		}
		out = append(out, child)
	}
	return out, nil
}

func (c *client) walk(ctx context.Context, root pageMeta, maxPages int, visit func(pageMeta, int) error) error {
	tree, err := c.pageTree(ctx, root.Library, root.ID)
	if err != nil {
		return c.walkSubpages(ctx, root, maxPages, visit)
	}

	seen := map[string]bool{}
	count := 0
	var dfs func(treePage, int) error
	dfs = func(node treePage, depth int) error {
		if maxPages > 0 && count >= maxPages {
			return nil
		}
		if seen[node.ID] {
			return nil
		}
		children, err := treeChildren(node)
		if err != nil {
			return err
		}
		seen[node.ID] = true
		count++
		meta := pageMeta{
			ID:       node.ID,
			Title:    node.Title,
			URI:      node.URI,
			Path:     node.Path.Text,
			Modified: node.Modified,
			Article:  node.Article,
			HasKids:  len(children) > 0,
			Library:  root.Library,
		}
		if depth == 0 {
			meta = root
			meta.HasKids = len(children) > 0
		}
		if err := visit(meta, depth); err != nil {
			return err
		}
		for _, child := range children {
			if maxPages > 0 && count >= maxPages {
				return nil
			}
			if err := dfs(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return dfs(tree.Page, 0)
}

func (c *client) pageTree(ctx context.Context, library, id string) (treeResponse, error) {
	u := fmt.Sprintf("https://%s.libretexts.org/@api/deki/pages/%s/tree?dream.out.format=json", library, url.PathEscape(id))
	resp, err := c.get(ctx, u)
	if err != nil {
		return treeResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return treeResponse{}, upstreamHTTPError(fmt.Sprintf("page tree failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	var tree treeResponse
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return treeResponse{}, err
	}
	if tree.Page.ID == "" {
		return treeResponse{}, newAgentError("INVALID_IDENTIFIER", "page tree did not include a root page", "Confirm the page identifier and retry.", false, nil)
	}
	return tree, nil
}

func treeChildren(page treePage) ([]treePage, error) {
	raw := strings.TrimSpace(string(page.Subpages))
	if raw == "" || raw == "null" || raw == `""` {
		return nil, nil
	}
	var group struct {
		Page json.RawMessage `json:"page"`
	}
	if err := json.Unmarshal(page.Subpages, &group); err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(string(group.Page))
	if raw == "" || raw == "null" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var children []treePage
		if err := json.Unmarshal(group.Page, &children); err != nil {
			return nil, err
		}
		return children, nil
	}
	var child treePage
	if err := json.Unmarshal(group.Page, &child); err != nil {
		return nil, err
	}
	return []treePage{child}, nil
}

func (c *client) walkSubpages(ctx context.Context, root pageMeta, maxPages int, visit func(pageMeta, int) error) error {
	seen := map[string]bool{}
	var count int
	var dfs func(pageMeta, int) error
	dfs = func(meta pageMeta, depth int) error {
		if maxPages > 0 && count >= maxPages {
			return nil
		}
		if seen[meta.ID] {
			return nil
		}
		seen[meta.ID] = true
		count++
		if err := visit(meta, depth); err != nil {
			return err
		}
		kids, err := c.subpages(ctx, meta)
		if err != nil {
			return err
		}
		for _, kid := range kids {
			if maxPages > 0 && count >= maxPages {
				return nil
			}
			if err := dfs(kid, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return dfs(root, 0)
}

func (c *client) pageIDFromHTML(ctx context.Context, pageURL string) (string, error) {
	resp, err := c.get(ctx, pageURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", upstreamHTTPError(fmt.Sprintf("HTML page fetch failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`<li[^>]+id=["']pageNumberHolder["'][\s\S]*?<dd>(\d+)</dd>`)
	m := re.FindSubmatch(b)
	if len(m) < 2 {
		return "", newAgentError("INVALID_IDENTIFIER", "could not find LibreTexts page ID in HTML", "Provide a page URL that exposes a LibreTexts page identifier.", false, nil)
	}
	return string(m[1]), nil
}

func (c *client) get(ctx context.Context, u string) (*http.Response, error) {
	return c.do(ctx, u, c.http)
}

// getLarge fetches with the download budget. Whole-book PDFs routinely exceed
// 100 MB, which does not fit in the per-request timeout.
func (c *client) getLarge(ctx context.Context, u string) (*http.Response, error) {
	if c.download == nil {
		return c.do(ctx, u, c.http)
	}
	return c.do(ctx, u, c.download)
}

func (c *client) do(ctx context.Context, u string, httpClient *http.Client) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json,text/html,application/pdf;q=0.9,*/*;q=0.8")
	return httpClient.Do(req)
}

// bookDownloadURL points at the Commons download service, which publishes the
// rendered PDF for a book or major section.
func bookDownloadURL(library, id string) string {
	return fmt.Sprintf("https://downloads.libretexts.org/api/v1/download/%s-%s/pdf", url.PathEscape(library), url.PathEscape(id))
}

// fetchPDF returns the body of the first endpoint that serves a PDF for the
// page. The Commons download service is tried first because the Deki
// contents.alt URL that page metadata advertises is frequently unavailable.
func (c *client) fetchPDF(ctx context.Context, meta pageMeta) (*http.Response, error) {
	candidates := []string{bookDownloadURL(meta.Library, meta.ID)}
	if meta.PDF != "" {
		candidates = append(candidates, meta.PDF)
	}
	var lastErr error
	for _, candidate := range candidates {
		resp, err := c.getLarge(ctx, candidate)
		if err != nil {
			if lastErr == nil {
				lastErr = classifyError(err)
			}
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
			return resp, nil
		}
		resp.Body.Close()
		// Candidates are ordered by authority, so the first failure is the one
		// worth reporting. The Deki fallback returns 500 for pages that simply
		// have no PDF, and letting that overwrite the download service's 404
		// would report a permanent condition as retryable.
		if lastErr != nil {
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			lastErr = newAgentError("PDF_UNAVAILABLE", fmt.Sprintf("no PDF is published for %s", cacheKey(meta.Library, meta.ID)), "LibreTexts renders PDFs for books and major sections, not every leaf page. Retry with a book or section root, which libretexts tree lists at the top.", false, nil)
			continue
		}
		lastErr = upstreamHTTPError(fmt.Sprintf("download failed: HTTP %d", resp.StatusCode), resp.StatusCode)
	}
	return nil, lastErr
}

func writeExtracted(filename, format string, item extractedPage) error {
	switch format {
	case "markdown", "md":
		body := fmt.Sprintf("# %s\n\nSource: %s\nPage ID: %s\n\n%s\n", item.Meta.Title, item.Meta.URI, item.Meta.ID, item.Content.Text)
		return os.WriteFile(filename, []byte(body), 0o644)
	case "html":
		body := fmt.Sprintf("<!doctype html><meta charset=\"utf-8\"><title>%s</title><h1>%s</h1><p><a href=\"%s\">Source</a></p>%s", stdhtml.EscapeString(item.Meta.Title), stdhtml.EscapeString(item.Meta.Title), stdhtml.EscapeString(item.Meta.URI), item.Content.HTML)
		return os.WriteFile(filename, []byte(body), 0o644)
	case "json":
		return writeJSONFile(filename, item)
	default:
		return newAgentError("INVALID_ARGUMENT", fmt.Sprintf("unknown format %q", format), "Use one of: markdown, md, html, or json.", false, nil)
	}
}

func extFor(format string) string {
	switch format {
	case "html":
		return "html"
	case "json":
		return "json"
	default:
		return "md"
	}
}

func contentBodyHTML(v any) string {
	switch b := v.(type) {
	case string:
		return b
	case []any:
		var parts []string
		for _, item := range b {
			switch x := item.(type) {
			case string:
				parts = append(parts, x)
			case map[string]any:
				parts = append(parts, asString(x["#text"]))
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func htmlText(s string) string {
	breaks := regexp.MustCompile(`(?i)</?(p|div|li|tr|h[1-6]|br|table|section|article)[^>]*>`)
	tags := regexp.MustCompile(`(?s)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>|<[^>]+>`)
	s = breaks.ReplaceAllString(s, "\n")
	s = tags.ReplaceAllString(s, " ")
	s = stdhtml.UnescapeString(s)
	lines := strings.Split(s, "\n")
	var clean []string
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			clean = append(clean, line)
		}
	}
	return strings.Join(clean, "\n\n")
}

func pageIDFromURLPath(p string) (string, bool) {
	parts := strings.Split(p, "/")
	for i := range parts {
		if parts[i] == "pages" && i+1 < len(parts) {
			if _, err := strconv.Atoi(parts[i+1]); err == nil {
				return parts[i+1], true
			}
		}
	}
	return "", false
}

func parseBookID(input string) (string, string, bool) {
	parts := strings.Split(input, "-")
	if len(parts) != 2 {
		return "", "", false
	}
	library, pageID := parts[0], parts[1]
	if library == "" || pageID == "" {
		return "", "", false
	}
	for _, r := range library {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return "", "", false
		}
	}
	if _, err := strconv.Atoi(pageID); err != nil {
		return "", "", false
	}
	return library, pageID, true
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeJSONFile(filename string, v any) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	return writeJSONAndClose(f, v)
}

func writeJSONAndClose(w io.WriteCloser, v any) error {
	writeErr := writeJSON(w, v)
	closeErr := w.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// knownLibrary reports whether name is one of the LibreTexts content libraries
// the libraries command publishes. Anything else resolves to a host that does
// not exist, which would otherwise surface as a retryable network error.
func knownLibrary(name string) bool {
	for _, library := range libraries {
		if library == name {
			return true
		}
	}
	return false
}

func unknownLibraryError(name string) *agentError {
	return newAgentError("UNKNOWN_LIBRARY", fmt.Sprintf("unknown LibreTexts library %q", name), fmt.Sprintf("Use one of: %s.", strings.Join(libraries, ", ")), false, nil)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// compact collapses runs of whitespace and truncates to n characters. It counts
// runes rather than bytes, so multi-byte text is never split mid-codepoint.
func compact(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	const ellipsis = "..."
	if n <= len(ellipsis) {
		return string(runes[:n])
	}
	return string(runes[:n-len(ellipsis)]) + ellipsis
}

func parseCommandFlags(fs *flag.FlagSet, args []string) error {
	type boolFlag interface {
		IsBoolFlag() bool
	}

	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}

	reordered := append(flags, "--")
	reordered = append(reordered, positionals...)
	return fs.Parse(reordered)
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
