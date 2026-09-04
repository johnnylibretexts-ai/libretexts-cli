package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const cliVersion = "0.1"

// maxSearchLimit caps how many catalog records one tool call may return, so a
// model cannot request a result set that swamps its own context.
const maxSearchLimit = 100

type emptyToolInput struct{}

type searchBooksInput struct {
	Query   string `json:"query" jsonschema:"textbook search query"`
	Library string `json:"library,omitempty" jsonschema:"LibreTexts library host, for example chem"`
	Limit   *int   `json:"limit,omitempty" jsonschema:"Maximum results; defaults to 10 when omitted; must be from 0 through 100."`
}

type pageToolInput struct {
	Identifier string `json:"identifier" jsonschema:"LibreTexts book ID, URL, or numeric page ID"`
	Library    string `json:"library,omitempty" jsonschema:"LibreTexts library host for numeric IDs"`
	Content    string `json:"content,omitempty" jsonschema:"Content representation: text (default), html, or metadata. Metadata rejects offset and max_chars; make separate calls for text and HTML."`
	Offset     *int   `json:"offset,omitempty" jsonschema:"Unicode character offset; defaults to 0; must be 0 or greater; not valid with metadata."`
	MaxChars   *int   `json:"max_chars,omitempty" jsonschema:"Maximum Unicode characters for text or html; defaults to 6000; must be from 1 through 50000; not valid with metadata."`
}

type treeToolInput struct {
	Identifier string `json:"identifier" jsonschema:"LibreTexts book ID, URL, or numeric page ID"`
	Library    string `json:"library,omitempty" jsonschema:"LibreTexts library host for numeric IDs"`
	MaxPages   *int   `json:"max_pages,omitempty" jsonschema:"Maximum pages returned; defaults to 50; must be from 1 through 500."`
}

type extractBookInput struct {
	Identifier string `json:"identifier" jsonschema:"LibreTexts book ID, URL, or numeric page ID"`
	Library    string `json:"library,omitempty" jsonschema:"LibreTexts library host for numeric IDs"`
	OutputDir  string `json:"output_dir" jsonschema:"Required relative output directory beneath the configured write root; absolute paths, lexical traversal, and existing symbolic links are rejected."`
	Format     string `json:"format,omitempty" jsonschema:"Extracted page format: markdown (default), md, html, or json."`
	MaxPages   *int   `json:"max_pages,omitempty" jsonschema:"Maximum pages extracted; defaults to 0 (no limit); must be 0 or greater."`
	DelayMS    *int   `json:"delay_ms,omitempty" jsonschema:"Delay between content requests in milliseconds; defaults to 100; must be from 0 through 60000."`
}

type downloadPDFInput struct {
	Identifier string `json:"identifier" jsonschema:"LibreTexts book ID, URL, or numeric page ID"`
	Library    string `json:"library,omitempty" jsonschema:"LibreTexts library host for numeric IDs"`
	OutputFile string `json:"output_file" jsonschema:"Required relative PDF path beneath the configured write root; absolute paths, lexical traversal, and existing symbolic links are rejected."`
}

type listLibrariesOutput struct {
	Libraries []string `json:"libraries"`
}

type searchBooksOutput struct {
	Count int        `json:"count"`
	Books []bookInfo `json:"books"`
}

type pageToolOutput struct {
	Page pageResult `json:"page"`
}

type treeToolOutput struct {
	Count int          `json:"count"`
	Pages []pageResult `json:"pages"`
}

func cmdServe(ctx context.Context, c *client, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mcpMode := fs.Bool("mcp", false, "serve the Model Context Protocol over stdio")
	writeRootArg := fs.String("write-root", "", "enable file-writing MCP tools within this directory")
	if err := fs.Parse(args); err != nil {
		return invalidArgumentError("serve", err)
	}
	if fs.NArg() != 0 {
		return newAgentError("INVALID_ARGUMENT", "serve does not accept positional arguments", "Use serve --mcp with an optional --write-root directory.", false, nil)
	}
	if !*mcpMode {
		return newAgentError("INVALID_ARGUMENT", "serve requires --mcp", "Retry with serve --mcp.", false, nil)
	}

	root := ""
	if *writeRootArg != "" {
		var err error
		root, err = resolveWriteRoot(*writeRootArg)
		if err != nil {
			return err
		}
	}
	return newMCPServer(c, root).Run(ctx, &mcp.StdioTransport{})
}

func newMCPServer(c *client, writeRoot string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "libretexts", Version: cliVersion}, nil)

	mcp.AddTool(server, readTool("libretexts_list_libraries", capabilitySummary("libraries")),
		func(context.Context, *mcp.CallToolRequest, emptyToolInput) (*mcp.CallToolResult, listLibrariesOutput, error) {
			return nil, listLibrariesOutput{Libraries: append([]string(nil), libraries...)}, nil
		})
	mcp.AddTool(server, readTool("libretexts_search_books", capabilitySummary("search")),
		func(ctx context.Context, _ *mcp.CallToolRequest, input searchBooksInput) (*mcp.CallToolResult, searchBooksOutput, error) {
			limit := 10
			if input.Limit != nil {
				limit = *input.Limit
			}
			if limit < 0 || limit > maxSearchLimit {
				return nil, searchBooksOutput{}, mcpToolError(newAgentError("INVALID_ARGUMENT", fmt.Sprintf("limit must be between 0 and %d, got %d", maxSearchLimit, limit), fmt.Sprintf("Provide a limit from 0 through %d.", maxSearchLimit), false, nil))
			}
			books, err := c.searchBooks(ctx, input.Query, input.Library, limit)
			if err != nil {
				return nil, searchBooksOutput{}, mcpToolError(err)
			}
			return nil, searchBooksOutput{Count: len(books), Books: books}, nil
		})
	mcp.AddTool(server, readTool("libretexts_get_page", capabilitySummary("page")),
		func(ctx context.Context, _ *mcp.CallToolRequest, input pageToolInput) (*mcp.CallToolResult, pageToolOutput, error) {
			content := input.Content
			if content == "" {
				content = "text"
			}
			if content == "both" {
				return nil, pageToolOutput{}, mcpToolError(newAgentError("INVALID_ARGUMENT", `content mode "both" is not supported by libretexts_get_page`, `Make separate libretexts_get_page calls with content "text" and content "html".`, false, nil))
			}
			if !validPageContent(content) {
				return nil, pageToolOutput{}, mcpToolError(newAgentError("INVALID_ARGUMENT", fmt.Sprintf("unknown content mode %q", content), "Use one of: metadata, text, or html.", false, nil))
			}
			if content == "metadata" && (input.Offset != nil || input.MaxChars != nil) {
				return nil, pageToolOutput{}, mcpToolError(newAgentError("INVALID_ARGUMENT", "offset and max_chars are not valid with metadata content", "Remove offset and max_chars from the metadata request.", false, nil))
			}
			offset := 0
			if input.Offset != nil {
				offset = *input.Offset
			}
			maxChars := 0
			if content != "metadata" {
				maxChars = 6000
				if input.MaxChars != nil {
					maxChars = *input.MaxChars
				}
				if maxChars < 1 || maxChars > 50000 {
					return nil, pageToolOutput{}, mcpToolError(newAgentError("INVALID_ARGUMENT", fmt.Sprintf("max_chars must be between 1 and 50000, got %d", maxChars), "Provide a max_chars value from 1 through 50000.", false, nil))
				}
			}
			page, err := getPage(ctx, c, pageRequest{
				Identifier: input.Identifier,
				Library:    input.Library,
				Content:    content,
				Offset:     offset,
				MaxChars:   maxChars,
			})
			if err != nil {
				return nil, pageToolOutput{}, mcpToolError(err)
			}
			return nil, pageToolOutput{Page: page}, nil
		})
	mcp.AddTool(server, readTool("libretexts_get_tree", capabilitySummary("tree")),
		func(ctx context.Context, _ *mcp.CallToolRequest, input treeToolInput) (*mcp.CallToolResult, treeToolOutput, error) {
			maxPages := 50
			if input.MaxPages != nil {
				maxPages = *input.MaxPages
			}
			if maxPages < 1 || maxPages > 500 {
				return nil, treeToolOutput{}, mcpToolError(newAgentError("INVALID_ARGUMENT", fmt.Sprintf("max_pages must be between 1 and 500, got %d", maxPages), "Provide a max_pages value from 1 through 500.", false, nil))
			}
			pages, err := getTree(ctx, c, input.Identifier, input.Library, maxPages)
			if err != nil {
				return nil, treeToolOutput{}, mcpToolError(err)
			}
			return nil, treeToolOutput{Count: len(pages), Pages: pages}, nil
		})

	if writeRoot != "" {
		mcp.AddTool(server, writeTool("libretexts_extract_book", capabilitySummary("extract")),
			func(ctx context.Context, _ *mcp.CallToolRequest, input extractBookInput) (*mcp.CallToolResult, extractResult, error) {
				if _, err := confinedOutputPath(writeRoot, input.OutputDir); err != nil {
					return nil, extractResult{}, mcpToolError(err)
				}
				if input.Format == "" {
					input.Format = "markdown"
				}
				maxPages := 0
				if input.MaxPages != nil {
					maxPages = *input.MaxPages
				}
				if maxPages < 0 {
					return nil, extractResult{}, mcpToolError(newAgentError("INVALID_ARGUMENT", fmt.Sprintf("max_pages must be non-negative, got %d", maxPages), "Provide zero for no limit or a positive maximum.", false, nil))
				}
				delay, err := mcpDelayDuration(input.DelayMS)
				if err != nil {
					return nil, extractResult{}, mcpToolError(err)
				}
				result, err := extractBook(ctx, c, extractRequest{
					Identifier: input.Identifier,
					Library:    input.Library,
					OutputDir:  input.OutputDir,
					WriteRoot:  writeRoot,
					Format:     input.Format,
					MaxPages:   maxPages,
					Delay:      delay,
					Progress:   nil,
				})
				if err != nil {
					return nil, extractResult{}, mcpToolError(err)
				}
				return nil, result, nil
			})
		mcp.AddTool(server, writeTool("libretexts_download_pdf", capabilitySummary("pdf")),
			func(ctx context.Context, _ *mcp.CallToolRequest, input downloadPDFInput) (*mcp.CallToolResult, downloadResult, error) {
				if _, err := confinedOutputPath(writeRoot, input.OutputFile); err != nil {
					return nil, downloadResult{}, mcpToolError(err)
				}
				result, err := downloadPDF(ctx, c, downloadRequest{
					Identifier: input.Identifier,
					Library:    input.Library,
					OutputFile: input.OutputFile,
					WriteRoot:  writeRoot,
				})
				if err != nil {
					return nil, downloadResult{}, mcpToolError(err)
				}
				return nil, result, nil
			})
	}

	return server
}

func mcpDelayDuration(delayMS *int) (time.Duration, error) {
	value := 100
	if delayMS != nil {
		value = *delayMS
	}
	if value < 0 || value > 60000 {
		return 0, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("delay_ms must be between 0 and 60000, got %d", value), "Provide a delay_ms value from 0 through 60000.", false, nil)
	}
	return time.Duration(value) * time.Millisecond, nil
}

func mcpToolError(err error) error {
	encoded, marshalErr := json.Marshal(classifyError(err))
	if marshalErr != nil {
		return errors.New(classifyError(marshalErr).Error())
	}
	return errors.New(string(encoded))
}

func readTool(name, description string) *mcp.Tool {
	destructive := false
	openWorld := true
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			OpenWorldHint:   &openWorld,
		},
	}
}

func writeTool(name, description string) *mcp.Tool {
	destructive := true
	openWorld := true
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			OpenWorldHint:   &openWorld,
		},
	}
}

func capabilitySummary(name string) string {
	for _, command := range canonicalCapabilities.Commands {
		if command.Name == name {
			return command.Summary
		}
	}
	return ""
}
