package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDescribeJSONIsCompleteVersionedCapabilityContract(t *testing.T) {
	var out bytes.Buffer
	if err := cmdDescribe([]string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var got capabilityManifest
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != "1" || got.Name != "libretexts" {
		t.Fatalf("manifest identity = %+v", got)
	}
	if got.Version != "0.1" {
		t.Fatalf("manifest version = %q, want 0.1", got.Version)
	}
	if len(got.GlobalOptions) != 1 || got.GlobalOptions[0].Name != "--json-errors" || got.GlobalOptions[0].Type != "boolean" || got.GlobalOptions[0].Default != false {
		t.Fatalf("global options = %+v, want --json-errors boolean default false", got.GlobalOptions)
	}
	byName := map[string]commandCapability{}
	for _, command := range got.Commands {
		if _, duplicate := byName[command.Name]; duplicate {
			t.Fatalf("duplicate command %q", command.Name)
		}
		byName[command.Name] = command
	}
	if len(byName) != 8 {
		t.Fatalf("command count = %d, want 8: %+v", len(byName), got.Commands)
	}

	tests := []struct {
		name, synopsis, summary, resultShape string
		positionals                          []string
		options                              []string
		outputModes                          []string
		usesNetwork, writesFiles             bool
	}{
		{"libraries", "libraries", "list known LibreTexts library hosts", "known library host names", nil, []string{"--json"}, []string{"human", "json"}, false, false},
		{"search", "search QUERY [--library chem]", "search the Commons textbook catalog", "matching Commons textbook records", []string{"QUERY"}, []string{"--library", "--limit", "--json"}, []string{"human", "json"}, true, false},
		{"page", "page BOOK_ID_OR_URL", "show page metadata and content", "page metadata and selected content representation", []string{"BOOK_ID_OR_URL"}, []string{"--library", "--json", "--html", "--content", "--max-chars", "--offset"}, []string{"human", "json"}, true, false},
		{"tree", "tree BOOK_ID_OR_URL", "list recursive textbook hierarchy", "ordered hierarchy of page metadata", []string{"BOOK_ID_OR_URL"}, []string{"--library", "--max-pages", "--json"}, []string{"human", "json"}, true, false},
		{"extract", "extract BOOK_ID_OR_URL --out DIR", "extract an entire textbook hierarchy", "extracted content files and index.json", []string{"BOOK_ID_OR_URL"}, []string{"--library", "--out", "--format", "--max-pages", "--delay"}, []string{"human"}, true, true},
		{"pdf", "pdf BOOK_ID_OR_URL --out FILE", "download the textbook PDF", "downloaded PDF file", []string{"BOOK_ID_OR_URL"}, []string{"--library", "--out"}, []string{"human"}, true, true},
		{"describe", "describe [--json]", "", "versioned command capability manifest", nil, []string{"--json"}, []string{"human", "json"}, false, false},
		{"serve", "serve --mcp", "", "stdio Model Context Protocol server", nil, []string{"--mcp", "--write-root"}, []string{"mcp"}, true, true},
	}
	for _, tt := range tests {
		command, ok := byName[tt.name]
		if !ok {
			t.Fatalf("missing command %q", tt.name)
		}
		if command.Synopsis != tt.synopsis || command.ResultShape != tt.resultShape || command.UsesNetwork != tt.usesNetwork || command.WritesFiles != tt.writesFiles || (tt.summary != "" && command.Summary != tt.summary) || (tt.summary == "" && command.Summary == "") {
			t.Fatalf("command %q contract = %+v", tt.name, command)
		}
		if !sameStrings(positionalNames(command.Positionals), tt.positionals) || !sameStrings(optionNames(command.Options), tt.options) || !sameStrings(command.OutputModes, tt.outputModes) {
			t.Fatalf("command %q arguments/options/modes = %+v", tt.name, command)
		}
	}

	pageContent := optionByName(t, byName["page"].Options, "--content")
	pageDefault, ok := pageContent.Default.(map[string]any)
	if !ok || pageDefault["plain"] != "text" || pageDefault["json"] != "both" || len(pageDefault) != 2 || !sameStrings(pageContent.Values, []string{"text", "html", "metadata", "both"}) {
		t.Fatalf("page --content contract = %+v", pageContent)
	}
	if optionByName(t, byName["extract"].Options, "--out").Required != true || optionByName(t, byName["extract"].Options, "--format").Default != "markdown" || !sameStrings(optionByName(t, byName["extract"].Options, "--format").Values, []string{"markdown", "md", "html", "json"}) {
		t.Fatalf("extract option contract = %+v", byName["extract"].Options)
	}
	if optionByName(t, byName["search"].Options, "--limit").Default != float64(10) || optionByName(t, byName["tree"].Options, "--max-pages").Default != float64(0) || optionByName(t, byName["serve"].Options, "--mcp").Default != false || !optionByName(t, byName["serve"].Options, "--mcp").Required {
		t.Fatalf("numeric/server defaults not preserved")
	}
}

func TestDescribeJSONDeclaresEveryOptionDefaultRequiredMarkerAndValueSet(t *testing.T) {
	var out bytes.Buffer
	if err := cmdDescribe([]string{"--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var got capabilityManifest
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	type expectedOption struct {
		name, typ    string
		defaultValue any
		required     bool
		values       []string
	}
	expectedGlobal := expectedOption{"--json-errors", "boolean", false, false, nil}
	if len(got.GlobalOptions) != 1 {
		t.Fatalf("global options = %+v, want one", got.GlobalOptions)
	}
	assertOptionContract(t, got.GlobalOptions[0], expectedGlobal)

	expected := map[string][]expectedOption{
		"libraries": {{"--json", "boolean", false, false, nil}},
		"search": {
			{"--library", "string", "", false, nil},
			{"--limit", "integer", float64(10), false, nil},
			{"--json", "boolean", false, false, nil},
		},
		"page": {
			{"--library", "string", "", false, nil},
			{"--json", "boolean", false, false, nil},
			{"--html", "boolean", false, false, nil},
			{"--content", "string", map[string]any{"plain": "text", "json": "both"}, false, []string{"text", "html", "metadata", "both"}},
			{"--max-chars", "integer", float64(0), false, nil},
			{"--offset", "integer", float64(0), false, nil},
		},
		"tree": {
			{"--library", "string", "", false, nil},
			{"--max-pages", "integer", float64(0), false, nil},
			{"--json", "boolean", false, false, nil},
		},
		"extract": {
			{"--library", "string", "", false, nil},
			{"--out", "string", nil, true, nil},
			{"--format", "string", "markdown", false, []string{"markdown", "md", "html", "json"}},
			{"--max-pages", "integer", float64(0), false, nil},
			{"--delay", "duration", "100ms", false, nil},
		},
		"pdf": {
			{"--library", "string", "", false, nil},
			{"--out", "string", "", false, nil},
		},
		"describe": {{"--json", "boolean", false, false, nil}},
		"serve": {
			{"--mcp", "boolean", false, true, nil},
			{"--write-root", "string", "", false, nil},
		},
	}
	for _, command := range got.Commands {
		want, ok := expected[command.Name]
		if !ok {
			t.Fatalf("unexpected command %q", command.Name)
		}
		if len(command.Options) != len(want) {
			t.Fatalf("command %q option count = %d, want %d", command.Name, len(command.Options), len(want))
		}
		for i, option := range command.Options {
			assertOptionContract(t, option, want[i])
		}
	}
}

func assertOptionContract(t *testing.T, got optionCapability, want struct {
	name, typ    string
	defaultValue any
	required     bool
	values       []string
}) {
	t.Helper()
	if got.Name != want.name || got.Type != want.typ || !reflect.DeepEqual(got.Default, want.defaultValue) || got.Required != want.required || !reflect.DeepEqual(got.Values, want.values) {
		t.Fatalf("option contract = %+v, want name=%q type=%q default=%#v required=%t values=%#v", got, want.name, want.typ, want.defaultValue, want.required, want.values)
	}
}

func TestHumanHelpRendersCanonicalHeadlineSummariesAndSynopses(t *testing.T) {
	var out bytes.Buffer
	if err := cmdDescribe(nil, &out); err != nil {
		t.Fatal(err)
	}
	human := out.String()
	for _, want := range []string{
		"libretexts - search and extract LibreTexts textbook content",
		"search QUERY [--library chem]",
		"search the Commons textbook catalog",
		"extract BOOK_ID_OR_URL --out DIR",
		"extract an entire textbook hierarchy",
		"pdf BOOK_ID_OR_URL --out FILE",
		"download the textbook PDF",
		"Use --json on search/page/tree for machine-readable output.",
		"describe --json",
		"--json-errors",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("human help missing %q:\n%s", want, human)
		}
	}

	usageOutput := captureStdout(t, usage)
	if usageOutput != human {
		t.Fatalf("usage output drifted from describe output:\nusage=%s\ndescribe=%s", usageOutput, human)
	}
}

func TestRunDispatchesDescribeAndRejectsInvalidArguments(t *testing.T) {
	err := run(context.Background(), []string{"describe", "unexpected"})
	if err == nil {
		t.Fatal("run(describe unexpected) returned nil")
	}
	coded, ok := err.(*agentError)
	if !ok || coded.Code != "INVALID_ARGUMENT" {
		t.Fatalf("run describe error = %#v, want INVALID_ARGUMENT", err)
	}

	var out bytes.Buffer
	err = cmdDescribe([]string{"--unknown"}, &out)
	if err == nil {
		t.Fatal("cmdDescribe(--unknown) returned nil")
	}
	coded, ok = err.(*agentError)
	if !ok || coded.Code != "INVALID_ARGUMENT" {
		t.Fatalf("cmdDescribe unknown flag error = %#v, want INVALID_ARGUMENT", err)
	}
}

func optionByName(t *testing.T, options []optionCapability, name string) optionCapability {
	t.Helper()
	for _, option := range options {
		if option.Name == name {
			return option
		}
	}
	t.Fatalf("missing option %q in %+v", name, options)
	return optionCapability{}
}

func optionNames(options []optionCapability) []string {
	names := make([]string, len(options))
	for i, option := range options {
		names[i] = option.Name
	}
	return names
}

func positionalNames(positionals []positionalCapability) []string {
	names := make([]string, len(positionals))
	for i, positional := range positionals {
		names[i] = positional.Name
	}
	return names
}

func sameStrings(got, want []string) bool {
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = previous }()

	fn()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}
