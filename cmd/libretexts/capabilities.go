package main

import (
	"flag"
	"fmt"
	"io"
)

type positionalCapability struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type optionCapability struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Default     any      `json:"default,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Values      []string `json:"values,omitempty"`
}

type commandCapability struct {
	Name        string                 `json:"name"`
	Synopsis    string                 `json:"synopsis"`
	Summary     string                 `json:"summary"`
	Positionals []positionalCapability `json:"positionals,omitempty"`
	Options     []optionCapability     `json:"options,omitempty"`
	OutputModes []string               `json:"output_modes"`
	ResultShape string                 `json:"result_shape"`
	UsesNetwork bool                   `json:"uses_network"`
	WritesFiles bool                   `json:"writes_files"`
}

type capabilityManifest struct {
	SchemaVersion string              `json:"schema_version"`
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	GlobalOptions []optionCapability  `json:"global_options"`
	Commands      []commandCapability `json:"commands"`
}

var canonicalCapabilities = capabilityManifest{
	SchemaVersion: "1",
	Name:          "libretexts",
	Version:       "0.1",
	GlobalOptions: []optionCapability{
		{Name: "--json-errors", Type: "boolean", Description: "emit one machine-readable JSON error object to stderr", Default: false},
	},
	Commands: []commandCapability{
		{
			Name: "libraries", Synopsis: "libraries", Summary: "list known LibreTexts library hosts",
			Options:     []optionCapability{{Name: "--json", Type: "boolean", Description: "emit JSON", Default: false}},
			OutputModes: []string{"human", "json"}, ResultShape: "known library host names", UsesNetwork: false, WritesFiles: false,
		},
		{
			Name: "search", Synopsis: "search QUERY [--library chem]", Summary: "search the Commons textbook catalog",
			Positionals: []positionalCapability{{Name: "QUERY", Description: "textbook search query", Required: true}},
			Options: []optionCapability{
				{Name: "--library", Type: "string", Description: "LibreTexts library host, e.g. chem", Default: ""},
				{Name: "--limit", Type: "integer", Description: "maximum results", Default: 10},
				{Name: "--json", Type: "boolean", Description: "emit JSON", Default: false},
			},
			OutputModes: []string{"human", "json"}, ResultShape: "matching Commons textbook records", UsesNetwork: true, WritesFiles: false,
		},
		{
			Name: "page", Synopsis: "page BOOK_ID_OR_URL", Summary: "show page metadata and content",
			Positionals: []positionalCapability{{Name: "BOOK_ID_OR_URL", Description: "LibreTexts book ID, URL, or numeric page ID with --library", Required: true}},
			Options: []optionCapability{
				{Name: "--library", Type: "string", Description: "LibreTexts library host for numeric IDs", Default: ""},
				{Name: "--json", Type: "boolean", Description: "emit JSON", Default: false},
				{Name: "--html", Type: "boolean", Description: "print content HTML", Default: false},
				{Name: "--content", Type: "string", Description: "select content representation; omitted defaults to text for human output and both for JSON", Default: map[string]string{"plain": "text", "json": "both"}, Values: []string{"text", "html", "metadata", "both"}},
				{Name: "--max-chars", Type: "integer", Description: "maximum Unicode characters returned", Default: 0},
				{Name: "--offset", Type: "integer", Description: "Unicode character offset for a bounded content stream", Default: 0},
			},
			OutputModes: []string{"human", "json"}, ResultShape: "page metadata and selected content representation", UsesNetwork: true, WritesFiles: false,
		},
		{
			Name: "tree", Synopsis: "tree BOOK_ID_OR_URL", Summary: "list recursive textbook hierarchy",
			Positionals: []positionalCapability{{Name: "BOOK_ID_OR_URL", Description: "LibreTexts book ID, URL, or numeric page ID with --library", Required: true}},
			Options: []optionCapability{
				{Name: "--library", Type: "string", Description: "LibreTexts library host for numeric IDs", Default: ""},
				{Name: "--max-pages", Type: "integer", Description: "stop after N pages, 0 means no limit", Default: 0},
				{Name: "--json", Type: "boolean", Description: "emit JSON", Default: false},
			},
			OutputModes: []string{"human", "json"}, ResultShape: "ordered hierarchy of page metadata", UsesNetwork: true, WritesFiles: false,
		},
		{
			Name: "extract", Synopsis: "extract BOOK_ID_OR_URL --out DIR", Summary: "extract an entire textbook hierarchy",
			Positionals: []positionalCapability{{Name: "BOOK_ID_OR_URL", Description: "LibreTexts book ID, URL, or numeric page ID with --library", Required: true}},
			Options: []optionCapability{
				{Name: "--library", Type: "string", Description: "LibreTexts library host for numeric IDs", Default: ""},
				{Name: "--out", Type: "string", Description: "output directory", Required: true},
				{Name: "--format", Type: "string", Description: "extracted page format", Default: "markdown", Values: []string{"markdown", "md", "html", "json"}},
				{Name: "--max-pages", Type: "integer", Description: "stop after N pages, 0 means no limit", Default: 0},
				{Name: "--delay", Type: "duration", Description: "delay between content requests", Default: "100ms"},
			},
			OutputModes: []string{"human"}, ResultShape: "extracted content files and index.json", UsesNetwork: true, WritesFiles: true,
		},
		{
			Name: "pdf", Synopsis: "pdf BOOK_ID_OR_URL --out FILE", Summary: "download the textbook PDF",
			Positionals: []positionalCapability{{Name: "BOOK_ID_OR_URL", Description: "LibreTexts book ID, URL, or numeric page ID with --library", Required: true}},
			Options: []optionCapability{
				{Name: "--library", Type: "string", Description: "LibreTexts library host for numeric IDs", Default: ""},
				{Name: "--out", Type: "string", Description: "output PDF path; defaults to the page title", Default: ""},
			},
			OutputModes: []string{"human"}, ResultShape: "downloaded PDF file", UsesNetwork: true, WritesFiles: true,
		},
		{
			Name: "describe", Synopsis: "describe [--json]", Summary: "describe CLI capabilities for humans and agents",
			Options:     []optionCapability{{Name: "--json", Type: "boolean", Description: "emit the versioned capability manifest", Default: false}},
			OutputModes: []string{"human", "json"}, ResultShape: "versioned command capability manifest", UsesNetwork: false, WritesFiles: false,
		},
	},
}

func cmdDescribe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("describe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "emit the versioned capability manifest")
	if err := fs.Parse(args); err != nil {
		return newAgentError("INVALID_ARGUMENT", err.Error(), "Use describe with no arguments or --json.", false, err)
	}
	if fs.NArg() != 0 {
		return newAgentError("INVALID_ARGUMENT", "describe does not accept positional arguments", "Use describe with no arguments or --json.", false, nil)
	}
	if *jsonOut {
		return writeJSON(out, canonicalCapabilities)
	}
	return renderHumanCapabilities(out, canonicalCapabilities)
}

func renderHumanCapabilities(out io.Writer, manifest capabilityManifest) error {
	if _, err := fmt.Fprintln(out, "libretexts - search and extract LibreTexts textbook content\n\nCommands:"); err != nil {
		return err
	}
	for _, command := range manifest.Commands {
		if _, err := fmt.Fprintf(out, "  %-34s %s\n", command.Synopsis, command.Summary); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out, "\nUse --json on search/page/tree for machine-readable output."); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, "Use describe --json for machine-readable capability discovery. Use --json-errors for machine-readable errors.")
	return err
}
