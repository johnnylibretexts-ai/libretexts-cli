package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type extractRequest struct {
	Identifier string
	Library    string
	OutputDir  string
	WriteRoot  string
	Format     string
	MaxPages   int
	Delay      time.Duration
	Progress   io.Writer
}

type extractResult struct {
	PageCount int    `json:"page_count"`
	OutputDir string `json:"output_dir"`
	IndexPath string `json:"index_path"`
}

type downloadRequest struct {
	Identifier string
	Library    string
	OutputFile string
	WriteRoot  string
}

type downloadResult struct {
	OutputFile string `json:"output_file"`
	Bytes      int64  `json:"bytes"`
}

type pageRequest struct {
	Identifier string
	Library    string
	Content    string
	Offset     int
	MaxChars   int
}

type pageResult struct {
	Meta    pageMeta       `json:"meta"`
	Content pageContent    `json:"content"`
	Depth   int            `json:"depth"`
	Window  *contentWindow `json:"window,omitempty"`
}

func getPage(ctx context.Context, c *client, request pageRequest) (pageResult, error) {
	if request.Offset < 0 {
		return pageResult{}, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("offset must be non-negative, got %d", request.Offset), "Provide an offset of zero or greater.", false, nil)
	}
	if request.MaxChars < 0 {
		return pageResult{}, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("max-chars must be non-negative, got %d", request.MaxChars), "Provide a max-chars value of zero or greater.", false, nil)
	}
	if !validPageContent(request.Content) {
		return pageResult{}, newAgentError("INVALID_ARGUMENT", fmt.Sprintf("unknown content mode %q", request.Content), "Use one of: metadata, text, html, or both.", false, nil)
	}
	if (request.Offset != 0 || request.MaxChars != 0) && request.Content != "text" && request.Content != "html" {
		return pageResult{}, newAgentError("INVALID_ARGUMENT", "offset and max-chars require --content text or --content html", "Select exactly one content representation before applying bounds.", false, nil)
	}

	meta, err := c.resolvePage(ctx, request.Identifier, request.Library)
	if err != nil {
		return pageResult{}, err
	}
	result := pageResult{Meta: meta}
	if request.Content == "metadata" {
		return result, nil
	}

	content, err := c.pageContent(ctx, meta.Library, meta.ID)
	if err != nil {
		return pageResult{}, err
	}

	switch request.Content {
	case "text":
		content.Text = htmlText(content.HTML)
		content.HTML = ""
		content.Text, err = selectPageContent(content.Text, request, &result)
		if err != nil {
			return pageResult{}, err
		}
	case "html":
		content.Text = ""
		content.HTML, err = selectPageContent(content.HTML, request, &result)
		if err != nil {
			return pageResult{}, err
		}
	case "both":
		content.Text = htmlText(content.HTML)
	}
	result.Content = content
	return result, nil
}

func selectPageContent(value string, request pageRequest, result *pageResult) (string, error) {
	selected, window, err := selectContent(value, request.Offset, request.MaxChars)
	if err != nil {
		return "", err
	}
	if request.Offset != 0 || request.MaxChars != 0 {
		result.Window = &window
	}
	return selected, nil
}

func validPageContent(content string) bool {
	switch content {
	case "metadata", "text", "html", "both":
		return true
	default:
		return false
	}
}

func getTree(ctx context.Context, c *client, identifier, library string, maxPages int) ([]pageResult, error) {
	root, err := c.resolvePage(ctx, identifier, library)
	if err != nil {
		return nil, err
	}
	var pages []pageResult
	err = c.walk(ctx, root, maxPages, func(meta pageMeta, depth int) error {
		pages = append(pages, pageResult{Meta: meta, Depth: depth})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pages, nil
}

func extractBook(ctx context.Context, c *client, request extractRequest) (extractResult, error) {
	root, err := c.resolvePage(ctx, request.Identifier, request.Library)
	if err != nil {
		return extractResult{}, err
	}

	writeRoot := ""
	outputDir := request.OutputDir
	if request.WriteRoot != "" {
		writeRoot, err = resolveWriteRoot(request.WriteRoot)
		if err != nil {
			return extractResult{}, err
		}
		outputDir, err = confinedOutputPath(writeRoot, request.OutputDir)
		if err != nil {
			return extractResult{}, err
		}
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return extractResult{}, filesystemError(err)
	}
	if writeRoot != "" {
		outputDir, err = confinedOutputPath(writeRoot, request.OutputDir)
		if err != nil {
			return extractResult{}, err
		}
	}

	var index []pageMeta
	count := 0
	err = c.walk(ctx, root, request.MaxPages, func(meta pageMeta, depth int) error {
		content, err := c.pageContent(ctx, meta.Library, meta.ID)
		if err != nil {
			return err
		}
		content.Text = htmlText(content.HTML)
		item := extractedPage{Meta: meta, Content: content, Depth: depth}
		name := fmt.Sprintf("%04d-%s.%s", count+1, slug(meta.Title), extFor(request.Format))
		pagePath := filepath.Join(outputDir, name)
		if writeRoot != "" {
			pagePath, err = confinedOutputPath(writeRoot, filepath.Join(request.OutputDir, name))
			if err != nil {
				return err
			}
		}
		if err := writeExtracted(pagePath, request.Format, item); err != nil {
			return classifyError(err)
		}
		index = append(index, meta)
		count++
		if err := waitForDelay(ctx, request.Delay); err != nil {
			return err
		}
		if request.Progress != nil {
			fmt.Fprintf(request.Progress, "extracted %d: %s\n", count, meta.Title)
		}
		return nil
	})
	if err != nil {
		return extractResult{}, err
	}

	indexPath := filepath.Join(outputDir, "index.json")
	if writeRoot != "" {
		indexPath, err = confinedOutputPath(writeRoot, filepath.Join(request.OutputDir, "index.json"))
		if err != nil {
			return extractResult{}, err
		}
	}
	if err := writeJSONFile(indexPath, index); err != nil {
		return extractResult{}, classifyError(err)
	}
	return extractResult{PageCount: count, OutputDir: outputDir, IndexPath: indexPath}, nil
}

func waitForDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func downloadPDF(ctx context.Context, c *client, request downloadRequest) (downloadResult, error) {
	meta, err := c.resolvePage(ctx, request.Identifier, request.Library)
	if err != nil {
		return downloadResult{}, err
	}
	target := request.OutputFile
	if target == "" {
		target = slug(meta.Title) + ".pdf"
	}
	requestedTarget := target
	writeRoot := ""
	if request.WriteRoot != "" {
		writeRoot, err = resolveWriteRoot(request.WriteRoot)
		if err != nil {
			return downloadResult{}, err
		}
		target, err = confinedOutputPath(writeRoot, requestedTarget)
		if err != nil {
			return downloadResult{}, err
		}
	}
	resp, err := c.fetchPDF(ctx, meta)
	if err != nil {
		return downloadResult{}, err
	}
	defer resp.Body.Close()

	if writeRoot != "" {
		target, err = confinedOutputPath(writeRoot, requestedTarget)
		if err != nil {
			return downloadResult{}, err
		}
	}
	f, err := os.Create(target)
	if err != nil {
		return downloadResult{}, filesystemError(err)
	}
	written, err := copyAndClose(f, resp.Body)
	if err != nil {
		return downloadResult{}, classifyError(err)
	}
	return downloadResult{OutputFile: target, Bytes: written}, nil
}

func copyAndClose(dst io.WriteCloser, src io.Reader) (int64, error) {
	written, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return written, copyErr
	}
	return written, closeErr
}
