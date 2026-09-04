package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newLongPageTestClient(t *testing.T, text string) (*client, *[]*http.Request) {
	t.Helper()
	encodedContent, err := json.Marshal(map[string]any{"@revision": "1", "@type": "text/html", "@title": "Test Book", "body": "<p>" + text + "</p>"})
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]*http.Request, 0)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Clone(req.Context()))
		body := `{"@id":"123","title":"Test Book","uri.ui":"https://chem.libretexts.org/test-book"}`
		if strings.HasSuffix(req.URL.Path, "/contents") {
			body = string(encodedContent)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	return &client{http: &http.Client{Transport: transport}}, &requests
}

func newLargeTreeTestClient(t *testing.T, pageCount int) (*client, *[]*http.Request) {
	t.Helper()
	children := make([]map[string]any, 0, pageCount-1)
	for i := 1; i < pageCount; i++ {
		children = append(children, map[string]any{
			"@id":      string(rune('a' + i)),
			"title":    "Child",
			"uri.ui":   "https://chem.libretexts.org/child",
			"subpages": "",
		})
	}
	encodedTree, err := json.Marshal(map[string]any{
		"page": map[string]any{
			"@id": "123", "title": "Root", "uri.ui": "https://chem.libretexts.org/root",
			"subpages": map[string]any{"page": children},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]*http.Request, 0)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Clone(req.Context()))
		body := `{"@id":"123","title":"Root","uri.ui":"https://chem.libretexts.org/root"}`
		if strings.HasSuffix(req.URL.Path, "/tree") {
			body = string(encodedTree)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	return &client{http: &http.Client{Transport: transport}}, &requests
}

func connectMCPForTest(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "libretexts-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPDefaultToolsAreReadOnly(t *testing.T) {
	c, _ := newTestClient(t)
	session := connectMCPForTest(t, newMCPServer(c, ""))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"libretexts_get_page", "libretexts_get_tree", "libretexts_list_libraries", "libretexts_search_books"}
	var got []string
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s is not declared read-only", tool.Name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestMCPDefaultToolContracts(t *testing.T) {
	c, _ := newTestClient(t)
	session := connectMCPForTest(t, newMCPServer(c, ""))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := map[string]string{
		"libretexts_list_libraries": "list known LibreTexts library hosts",
		"libretexts_search_books":   "search the Commons textbook catalog",
		"libretexts_get_page":       "show page metadata and content",
		"libretexts_get_tree":       "list recursive textbook hierarchy",
	}
	for _, tool := range listed.Tools {
		if tool.Description != descriptions[tool.Name] {
			t.Fatalf("tool %s description = %q, want %q", tool.Name, tool.Description, descriptions[tool.Name])
		}
		annotations := tool.Annotations
		if annotations == nil || !annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint || !annotations.IdempotentHint || annotations.OpenWorldHint == nil || !*annotations.OpenWorldHint {
			t.Fatalf("tool %s annotations = %+v", tool.Name, annotations)
		}
		schema, ok := tool.OutputSchema.(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("tool %s output schema = %#v, want object schema", tool.Name, tool.OutputSchema)
		}
	}
}

func TestMCPPublishedSixToolContracts(t *testing.T) {
	c, _ := newTestClient(t)
	root, err := resolveWriteRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := connectMCPForTest(t, newMCPServer(c, root))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	type contract struct {
		description  string
		readOnly     bool
		destructive  bool
		resultFields []string
	}
	want := map[string]contract{
		"libretexts_list_libraries": {"list known LibreTexts library hosts", true, false, []string{"libraries"}},
		"libretexts_search_books":   {"search the Commons textbook catalog", true, false, []string{"books", "count"}},
		"libretexts_get_page":       {"show page metadata and content", true, false, []string{"page"}},
		"libretexts_get_tree":       {"list recursive textbook hierarchy", true, false, []string{"count", "pages"}},
		"libretexts_extract_book":   {"extract an entire textbook hierarchy", false, true, []string{"index_path", "output_dir", "page_count"}},
		"libretexts_download_pdf":   {"download the textbook PDF", false, true, []string{"bytes", "output_file"}},
	}
	if len(listed.Tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(listed.Tools), len(want))
	}
	for _, tool := range listed.Tools {
		contract, ok := want[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if tool.Description != contract.description {
			t.Fatalf("tool %s description = %q, want %q", tool.Name, tool.Description, contract.description)
		}
		annotations := tool.Annotations
		if annotations == nil || annotations.ReadOnlyHint != contract.readOnly || annotations.DestructiveHint == nil || *annotations.DestructiveHint != contract.destructive || !annotations.IdempotentHint || annotations.OpenWorldHint == nil || !*annotations.OpenWorldHint {
			t.Fatalf("tool %s annotations = %+v", tool.Name, annotations)
		}
		schema := schemaMap(t, tool.OutputSchema)
		if schema["type"] != "object" {
			t.Fatalf("tool %s output type = %v, want object", tool.Name, schema["type"])
		}
		if got := sortedSchemaKeys(t, schema["properties"]); !slices.Equal(got, contract.resultFields) {
			t.Fatalf("tool %s output fields = %v, want %v", tool.Name, got, contract.resultFields)
		}
		if got := sortedSchemaStrings(t, schema["required"]); !slices.Equal(got, contract.resultFields) {
			t.Fatalf("tool %s required result fields = %v, want %v", tool.Name, got, contract.resultFields)
		}
	}
}

func TestMCPPublishedInputSchemasDescribeDefaultsRangesAndWritePaths(t *testing.T) {
	c, _ := newTestClient(t)
	root, err := resolveWriteRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := connectMCPForTest(t, newMCPServer(c, root))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		"libretexts_search_books": {
			"limit": "Maximum results; defaults to 10 when omitted; must be from 0 through 100.",
		},
		"libretexts_get_page": {
			"content":   "Content representation: text (default), html, or metadata. Metadata rejects offset and max_chars; make separate calls for text and HTML.",
			"offset":    "Unicode character offset; defaults to 0; must be 0 or greater; not valid with metadata.",
			"max_chars": "Maximum Unicode characters for text or html; defaults to 6000; must be from 1 through 50000; not valid with metadata.",
		},
		"libretexts_get_tree": {
			"max_pages": "Maximum pages returned; defaults to 50; must be from 1 through 500.",
		},
		"libretexts_extract_book": {
			"output_dir": "Required relative output directory beneath the configured write root; absolute paths, lexical traversal, and existing symbolic links are rejected.",
			"max_pages":  "Maximum pages extracted; defaults to 0 (no limit); must be 0 or greater.",
			"delay_ms":   "Delay between content requests in milliseconds; defaults to 100; must be from 0 through 60000.",
		},
		"libretexts_download_pdf": {
			"output_file": "Required relative PDF path beneath the configured write root; absolute paths, lexical traversal, and existing symbolic links are rejected.",
		},
	}
	wantRequired := map[string][]string{
		"libretexts_list_libraries": nil,
		"libretexts_search_books":   {"query"},
		"libretexts_get_page":       {"identifier"},
		"libretexts_get_tree":       {"identifier"},
		"libretexts_extract_book":   {"identifier", "output_dir"},
		"libretexts_download_pdf":   {"identifier", "output_file"},
	}
	seen := make(map[string]bool, len(want))
	for _, tool := range listed.Tools {
		inputSchema := schemaMap(t, tool.InputSchema)
		if got := sortedOptionalSchemaStrings(t, inputSchema["required"]); !slices.Equal(got, wantRequired[tool.Name]) {
			t.Fatalf("tool %s required input fields = %v, want %v", tool.Name, got, wantRequired[tool.Name])
		}
		fields, ok := want[tool.Name]
		if !ok {
			continue
		}
		seen[tool.Name] = true
		properties := schemaMap(t, inputSchema["properties"])
		for field, description := range fields {
			property := schemaMap(t, properties[field])
			if property["description"] != description {
				t.Fatalf("tool %s field %s description = %q, want %q", tool.Name, field, property["description"], description)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("missing tool schema %q", name)
		}
	}
}

func schemaMap(t *testing.T, value any) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema value = %#v (%T), want object", value, value)
	}
	return got
}

func sortedSchemaKeys(t *testing.T, value any) []string {
	t.Helper()
	properties := schemaMap(t, value)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func sortedSchemaStrings(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("schema string list = %#v (%T), want array", value, value)
	}
	values := make([]string, len(raw))
	for i, item := range raw {
		var stringOK bool
		values[i], stringOK = item.(string)
		if !stringOK {
			t.Fatalf("schema string item = %#v (%T)", item, item)
		}
	}
	slices.Sort(values)
	return values
}

func sortedOptionalSchemaStrings(t *testing.T, value any) []string {
	t.Helper()
	if value == nil {
		return nil
	}
	return sortedSchemaStrings(t, value)
}

func TestMCPWriteRootEnablesDestructiveTools(t *testing.T) {
	c, _ := newTestClient(t)
	root, err := resolveWriteRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := connectMCPForTest(t, newMCPServer(c, root))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]bool{"libretexts_extract_book": false, "libretexts_download_pdf": false}
	for _, tool := range listed.Tools {
		if _, ok := writes[tool.Name]; !ok {
			continue
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Fatalf("unsafe annotations for %s: %+v", tool.Name, tool.Annotations)
		}
		writes[tool.Name] = true
	}
	for name, found := range writes {
		if !found {
			t.Fatalf("write tool %s not registered", name)
		}
	}
}

func TestMCPCallReturnsStructuredLibraries(t *testing.T) {
	c, _ := newTestClient(t)
	session := connectMCPForTest(t, newMCPServer(c, ""))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "libretexts_list_libraries"})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if result.IsError || !ok || len(structured["libraries"].([]any)) != len(libraries) {
		t.Fatalf("tool result = %+v", result)
	}
}

func TestMCPToolErrorContainsStableCode(t *testing.T) {
	c, _ := newTestClient(t)
	session := connectMCPForTest(t, newMCPServer(c, ""))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_get_page",
		Arguments: map[string]any{
			"identifier": "123",
			"library":    "chem",
			"offset":     999,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "INVALID_ARGUMENT") {
		t.Fatalf("tool error = %+v", result)
	}
}

func TestMCPPageSupportsEachAdvertisedContentMode(t *testing.T) {
	tests := []struct {
		mode         string
		wantText     string
		wantHTML     string
		wantRequests int
	}{
		{mode: "text", wantText: "Chapter text", wantRequests: 2},
		{mode: "html", wantHTML: "<p>Chapter text</p>", wantRequests: 2},
		{mode: "metadata", wantRequests: 1},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			c, requests := newTestClient(t)
			session := connectMCPForTest(t, newMCPServer(c, ""))
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "libretexts_get_page",
				Arguments: map[string]any{
					"identifier": "123",
					"library":    "chem",
					"content":    tt.mode,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || result.IsError {
				t.Fatalf("%s result = %+v", tt.mode, result)
			}
			page := result.StructuredContent.(map[string]any)["page"].(map[string]any)
			content := page["content"].(map[string]any)
			gotText, _ := content["text"].(string)
			gotHTML, _ := content["html"].(string)
			if gotText != tt.wantText || gotHTML != tt.wantHTML {
				t.Fatalf("%s content = %+v, want text %q and HTML %q", tt.mode, content, tt.wantText, tt.wantHTML)
			}
			if len(*requests) != tt.wantRequests {
				t.Fatalf("%s made %d requests, want %d", tt.mode, len(*requests), tt.wantRequests)
			}
		})
	}
}

func TestMCPPageRejectsBothWithStableErrorBeforeNetwork(t *testing.T) {
	c, requests := newTestClient(t)
	session := connectMCPForTest(t, newMCPServer(c, ""))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_get_page",
		Arguments: map[string]any{
			"identifier": "123",
			"library":    "chem",
			"content":    "both",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeMCPAgentError(t, result)
	if got.Code != "INVALID_ARGUMENT" || got.Message != `content mode "both" is not supported by libretexts_get_page` || got.Hint != `Make separate libretexts_get_page calls with content "text" and content "html".` || got.Retryable {
		t.Fatalf("tool error = %+v", got)
	}
	if len(*requests) != 0 {
		t.Fatalf("rejected mode made %d requests, want 0", len(*requests))
	}
}

func TestMCPPageMetadataRejectsExplicitBoundsAsIrrelevant(t *testing.T) {
	for _, tt := range []struct {
		name     string
		argument string
		value    int
	}{
		{name: "offset", argument: "offset", value: 0},
		{name: "max_chars", argument: "max_chars", value: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, requests := newTestClient(t)
			session := connectMCPForTest(t, newMCPServer(c, ""))
			arguments := map[string]any{"identifier": "123", "library": "chem", "content": "metadata", tt.argument: tt.value}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "libretexts_get_page", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeMCPAgentError(t, result)
			if got.Code != "INVALID_ARGUMENT" || got.Message != "offset and max_chars are not valid with metadata content" || got.Hint != "Remove offset and max_chars from the metadata request." || got.Retryable {
				t.Fatalf("tool error = %+v", got)
			}
			if len(*requests) != 0 {
				t.Fatalf("rejected metadata bound made %d requests, want 0", len(*requests))
			}
		})
	}
}

func decodeMCPAgentError(t *testing.T, result *mcp.CallToolResult) agentError {
	t.Helper()
	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("tool result = %+v, want one error text item", result)
	}
	textContent, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("tool error content = %T, want *mcp.TextContent", result.Content[0])
	}
	var got agentError
	if err := json.Unmarshal([]byte(textContent.Text), &got); err != nil {
		t.Fatalf("tool error text is not agent-error JSON: %q (%v)", textContent.Text, err)
	}
	return got
}

func TestMCPSearchLimitDistinguishesOmittedZeroAndNegative(t *testing.T) {
	tests := []struct {
		name      string
		limit     any
		wantCount float64
		wantError bool
	}{
		{name: "omitted", wantCount: 1},
		{name: "zero", limit: 0, wantCount: 0},
		{name: "negative", limit: -1, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, requests := newTestClient(t)
			session := connectMCPForTest(t, newMCPServer(c, ""))
			arguments := map[string]any{"query": "chemistry"}
			if tt.limit != nil {
				arguments["limit"] = tt.limit
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "libretexts_search_books", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantError {
				if !result.IsError || len(*requests) != 0 {
					t.Fatalf("result = %+v, requests = %d, want preflight error", result, len(*requests))
				}
				return
			}
			structured := result.StructuredContent.(map[string]any)
			if result.IsError || structured["count"] != tt.wantCount || len(*requests) != 1 {
				t.Fatalf("result = %+v, requests = %d", result, len(*requests))
			}
			wantLimit := "10"
			if tt.name == "zero" {
				wantLimit = "0"
			}
			if got := (*requests)[0].URL.Query().Get("limit"); got != wantLimit {
				t.Fatalf("request limit = %q, want %q", got, wantLimit)
			}
		})
	}
}

func TestMCPPageMaxCharsDefaultsAndBoundaries(t *testing.T) {
	c, _ := newLongPageTestClient(t, strings.Repeat("x", 6001))
	session := connectMCPForTest(t, newMCPServer(c, ""))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_get_page", Arguments: map[string]any{"identifier": "123", "library": "chem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := result.StructuredContent.(map[string]any)["page"].(map[string]any)
	content := page["content"].(map[string]any)
	window := page["window"].(map[string]any)
	if result.IsError || len([]rune(content["text"].(string))) != 6000 || window["next_offset"] != float64(6000) {
		t.Fatalf("default page result = %+v", result)
	}

	tests := []struct {
		value     int
		wantError bool
	}{
		{value: -1, wantError: true},
		{value: 0, wantError: true},
		{value: 1},
		{value: 50000},
		{value: 50001, wantError: true},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.value), func(t *testing.T) {
			c, requests := newTestClient(t)
			session := connectMCPForTest(t, newMCPServer(c, ""))
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "libretexts_get_page",
				Arguments: map[string]any{"identifier": "123", "library": "chem", "max_chars": tt.value},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != tt.wantError {
				t.Fatalf("max_chars %d result = %+v", tt.value, result)
			}
			if tt.wantError && len(*requests) != 0 {
				t.Fatalf("max_chars %d made %d requests before validation", tt.value, len(*requests))
			}
		})
	}
}

func TestMCPPageOffsetOmittedZeroAndNegative(t *testing.T) {
	for _, tt := range []struct {
		name      string
		offset    any
		wantError bool
	}{{name: "omitted"}, {name: "zero", offset: 0}, {name: "negative", offset: -1, wantError: true}} {
		t.Run(tt.name, func(t *testing.T) {
			c, requests := newTestClient(t)
			session := connectMCPForTest(t, newMCPServer(c, ""))
			arguments := map[string]any{"identifier": "123", "library": "chem"}
			if tt.offset != nil {
				arguments["offset"] = tt.offset
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "libretexts_get_page", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != tt.wantError {
				t.Fatalf("result = %+v", result)
			}
			if tt.wantError && len(*requests) != 0 {
				t.Fatalf("negative offset made %d requests", len(*requests))
			}
		})
	}
}

func TestMCPTreeMaxPagesDefaultsAndBoundaries(t *testing.T) {
	c, _ := newLargeTreeTestClient(t, 51)
	session := connectMCPForTest(t, newMCPServer(c, ""))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_get_tree", Arguments: map[string]any{"identifier": "123", "library": "chem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.StructuredContent.(map[string]any)["count"] != float64(50) {
		t.Fatalf("default tree result = %+v", result)
	}

	for _, tt := range []struct {
		value     int
		wantError bool
	}{{-1, true}, {0, true}, {500, false}, {501, true}} {
		t.Run(strconv.Itoa(tt.value), func(t *testing.T) {
			c, requests := newTestClient(t)
			session := connectMCPForTest(t, newMCPServer(c, ""))
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "libretexts_get_tree",
				Arguments: map[string]any{"identifier": "123", "library": "chem", "max_pages": tt.value},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != tt.wantError {
				t.Fatalf("max_pages %d result = %+v", tt.value, result)
			}
			if tt.wantError && len(*requests) != 0 {
				t.Fatalf("max_pages %d made %d requests before validation", tt.value, len(*requests))
			}
		})
	}
}

func TestMCPOptionalIntegerDecodingDistinguishesOmittedAndZero(t *testing.T) {
	var searchOmitted, searchExplicit searchBooksInput
	if err := json.Unmarshal([]byte(`{"query":"chemistry"}`), &searchOmitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"query":"chemistry","limit":0}`), &searchExplicit); err != nil {
		t.Fatal(err)
	}
	if searchOmitted.Limit != nil || searchExplicit.Limit == nil || *searchExplicit.Limit != 0 {
		t.Fatalf("search limits = omitted %v, explicit %v", searchOmitted.Limit, searchExplicit.Limit)
	}

	var pageOmitted, pageExplicit pageToolInput
	if err := json.Unmarshal([]byte(`{"identifier":"123"}`), &pageOmitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"identifier":"123","offset":0,"max_chars":0}`), &pageExplicit); err != nil {
		t.Fatal(err)
	}
	if pageOmitted.Offset != nil || pageOmitted.MaxChars != nil || pageExplicit.Offset == nil || *pageExplicit.Offset != 0 || pageExplicit.MaxChars == nil || *pageExplicit.MaxChars != 0 {
		t.Fatalf("page integers = omitted (%v, %v), explicit (%v, %v)", pageOmitted.Offset, pageOmitted.MaxChars, pageExplicit.Offset, pageExplicit.MaxChars)
	}

	var treeOmitted, treeExplicit treeToolInput
	if err := json.Unmarshal([]byte(`{"identifier":"123"}`), &treeOmitted); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"identifier":"123","max_pages":0}`), &treeExplicit); err != nil {
		t.Fatal(err)
	}
	if treeOmitted.MaxPages != nil || treeExplicit.MaxPages == nil || *treeExplicit.MaxPages != 0 {
		t.Fatalf("tree max_pages = omitted %v, explicit %v", treeOmitted.MaxPages, treeExplicit.MaxPages)
	}

	var omitted extractBookInput
	if err := json.Unmarshal([]byte(`{"identifier":"123","output_dir":"book"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.MaxPages != nil || omitted.DelayMS != nil {
		t.Fatalf("omitted integers = max_pages %v, delay_ms %v; want nil", omitted.MaxPages, omitted.DelayMS)
	}

	var explicit extractBookInput
	if err := json.Unmarshal([]byte(`{"identifier":"123","output_dir":"book","max_pages":0,"delay_ms":0}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.MaxPages == nil || *explicit.MaxPages != 0 || explicit.DelayMS == nil || *explicit.DelayMS != 0 {
		t.Fatalf("explicit integers = max_pages %v, delay_ms %v; want pointers to zero", explicit.MaxPages, explicit.DelayMS)
	}
}

func TestMCPDelayDurationDefaultsZeroAndBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		value     *int
		want      time.Duration
		wantError bool
	}{
		{name: "omitted", want: 100 * time.Millisecond},
		{name: "negative", value: intPointer(-1), wantError: true},
		{name: "zero", value: intPointer(0), want: 0},
		{name: "maximum", value: intPointer(60000), want: 60 * time.Second},
		{name: "above maximum", value: intPointer(60001), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mcpDelayDuration(tt.value)
			if (err != nil) != tt.wantError || got != tt.want {
				t.Fatalf("mcpDelayDuration(%v) = (%v, %v), want (%v, error=%t)", tt.value, got, err, tt.want, tt.wantError)
			}
			if tt.wantError {
				assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
			}
		})
	}
}

func TestMCPExtractRejectsInvalidDelayBeforeNetwork(t *testing.T) {
	for _, delayMS := range []int{-1, 60001} {
		t.Run(strconv.Itoa(delayMS), func(t *testing.T) {
			c, requests := newTestClient(t)
			root, err := resolveWriteRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session := connectMCPForTest(t, newMCPServer(c, root))
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "libretexts_extract_book",
				Arguments: map[string]any{
					"identifier": "123", "library": "chem", "output_dir": "book", "max_pages": 1, "delay_ms": delayMS,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || len(*requests) != 0 {
				t.Fatalf("delay_ms %d result = %+v, requests = %d", delayMS, result, len(*requests))
			}
		})
	}
}

func TestMCPExtractMaxPagesPreservesUnboundedOmittedAndZero(t *testing.T) {
	for _, tt := range []struct {
		name      string
		maxPages  any
		wantCount float64
		wantError bool
	}{
		{name: "omitted", wantCount: 2},
		{name: "zero", maxPages: 0, wantCount: 2},
		{name: "negative", maxPages: -1, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, requests := newTestClient(t)
			root, err := resolveWriteRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session := connectMCPForTest(t, newMCPServer(c, root))
			arguments := map[string]any{
				"identifier": "123", "library": "chem", "output_dir": "book", "delay_ms": 0,
			}
			if tt.maxPages != nil {
				arguments["max_pages"] = tt.maxPages
			}
			result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "libretexts_extract_book", Arguments: arguments})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != tt.wantError {
				t.Fatalf("result = %+v", result)
			}
			if tt.wantError {
				if len(*requests) != 0 {
					t.Fatalf("negative max_pages made %d requests", len(*requests))
				}
				return
			}
			if got := result.StructuredContent.(map[string]any)["page_count"]; got != tt.wantCount {
				t.Fatalf("page_count = %v, want %v", got, tt.wantCount)
			}
		})
	}
}

func TestMCPExtractBookWritesWithinRoot(t *testing.T) {
	c, _ := newTestClient(t)
	root, err := resolveWriteRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := connectMCPForTest(t, newMCPServer(c, root))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_extract_book",
		Arguments: map[string]any{
			"identifier": "123",
			"library":    "chem",
			"output_dir": "book",
			"format":     "markdown",
			"max_pages":  1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if result.IsError || !ok || structured["page_count"] != float64(1) {
		t.Fatalf("tool result = %+v", result)
	}
	for _, name := range []string{"index.json", "0001-test-book.md"} {
		if _, err := os.Stat(filepath.Join(root, "book", name)); err != nil {
			t.Fatalf("expected extracted file %s: %v", name, err)
		}
	}
}

func TestMCPDownloadPDFWritesWithinRoot(t *testing.T) {
	c, _ := newTestClient(t)
	root, err := resolveWriteRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session := connectMCPForTest(t, newMCPServer(c, root))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_download_pdf",
		Arguments: map[string]any{
			"identifier":  "123",
			"library":     "chem",
			"output_file": "book.pdf",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(filepath.Join(root, "book.pdf"))
	if result.IsError || readErr != nil || string(data) != "%PDF-1.4 test" {
		t.Fatalf("tool result = %+v, file = %q, read error = %v", result, data, readErr)
	}
}

func TestMCPDownloadPDFRejectsEscape(t *testing.T) {
	c, _ := newTestClient(t)
	container := t.TempDir()
	root, err := resolveWriteRoot(filepath.Join(container, "write"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(container, "escape.pdf")
	session := connectMCPForTest(t, newMCPServer(c, root))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "libretexts_download_pdf",
		Arguments: map[string]any{
			"identifier":  "123",
			"library":     "chem",
			"output_file": "../escape.pdf",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "INVALID_ARGUMENT") {
		t.Fatalf("tool error = %+v", result)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file exists or stat failed unexpectedly: %v", err)
	}
}

func TestCmdServeRequiresMCP(t *testing.T) {
	c, _ := newTestClient(t)
	err := cmdServe(context.Background(), c, nil)
	assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
}

func TestCmdServeRejectsUnknownAndPositionalArguments(t *testing.T) {
	c, _ := newTestClient(t)
	for _, args := range [][]string{{"--unknown"}, {"--mcp", "extra"}} {
		err := cmdServe(context.Background(), c, args)
		assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
	}
}

func TestCmdServeResolvesWriteRootBeforeProtocolStarts(t *testing.T) {
	c, _ := newTestClient(t)
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := cmdServe(context.Background(), c, []string{"--mcp", "--write-root", filepath.Join(file, "child")})
	assertAgentErrorCode(t, err, "FILESYSTEM_ERROR")
}

func TestRunDispatchesServe(t *testing.T) {
	err := run(context.Background(), []string{"serve"})
	assertAgentErrorCode(t, err, "INVALID_ARGUMENT")
}

func TestMCPCommandTransportSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	bin := filepath.Join(t.TempDir(), "libretexts")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, output)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "process-smoke", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{
		Command: exec.CommandContext(ctx, bin, "serve", "--mcp"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close MCP session: %v", err)
		}
	})
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 4 {
		t.Fatalf("listed tools = %+v, err = %v", listed, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "libretexts_list_libraries"})
	if err != nil || result == nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("call result = %+v, err = %v", result, err)
	}
}
