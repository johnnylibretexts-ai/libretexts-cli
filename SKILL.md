---
name: libretexts
description: Search LibreTexts open textbook libraries, inspect book structures and page hierarchies, retrieve page content or metadata, extract entire books/chapters into Markdown, HTML, or JSON, and download textbook PDFs using the libretexts CLI.
---

# LibreTexts CLI

Agent-friendly CLI for searching LibreTexts libraries, inspecting book trees, and extracting Open Educational Resources (OER) textbook content.

## Setup & Verification

Build the source checkout into a local binary:

```bash
go build -o libretexts ./cmd/libretexts
./libretexts --help
```

This source checkout does not itself establish an installation. Use
`./libretexts` after building, or `libretexts` only when a separately installed
binary is already on `PATH`.

Run the complete local check suite with:

```bash
make verify
```

It formats-checks `cmd/libretexts`, runs `go vet`, runs tests without cache
reuse, and builds the binary. It neither installs nor publishes the result.

## Common Workflows

### 1. List Available Libraries

LibreTexts organizes content by domain libraries (e.g., `chem`, `bio`, `math`, `phys`, `socialsci`):

```bash
libretexts libraries
libretexts libraries --json
```

### 2. Search Textbooks in the Commons Catalog

Search for textbooks by subject, title, or keywords:

```bash
# General search
libretexts search "general chemistry" --limit 5

# Library-specific search with JSON output
libretexts search "organic chemistry" --library chem --limit 5 --json
libretexts search "calculus" --library math --limit 5 --json
```

Results return metadata including `bookID` (e.g., `chem-38132`), title, author, and online/PDF links.

### 3. Inspect Textbook Table of Contents and Hierarchy (`tree`)

Retrieve the full chapter and page hierarchy of a book:

```bash
# Print tree hierarchy
libretexts tree chem-38132

# Bound tree traversal
libretexts tree chem-38132 --max-pages 30

# Output tree as JSON
libretexts tree chem-38132 --json
```

### 4. Fetch Page Content and Metadata (`page`)

Fetch single page details, plain text, or full HTML:

```bash
# View text content
libretexts page chem-38132

# View raw HTML (preserves math, tables, figures)
libretexts page chem-38132 --html

# Fetch page metadata and content as JSON
libretexts page chem-38132 --json

# Query using a full URL or numeric ID with --library
libretexts page "https://chem.libretexts.org/Bookshelves/General_Chemistry/Map%3A_Chemistry_(Zumdahl_and_Zumdahl)"
libretexts page 38132 --library chem --json
```

### 5. Extract Entire Textbook or Chapters (`extract`)

Batch extract all pages from a textbook hierarchy to disk:

```bash
# Extract normalized Markdown reading copies
libretexts extract chem-38132 --out ./extracted-book --format markdown

# Extract HTML copies (best for Canvas LMS course imports)
libretexts extract chem-38132 --out ./extracted-book --format html

# Extract JSON representations
libretexts extract chem-38132 --out ./extracted-book --format json

# Limit extraction depth/count and customize request rate
libretexts extract chem-38132 --out ./sample --format html --max-pages 10 --delay 200ms
```

Extraction writes:
- Numbered content files (e.g. `0001-chapter-1.html`)
- An `index.json` manifest recording page metadata, hierarchy depth, and URLs.

### 6. Download Publisher PDF (`pdf`)

Download the official compiled textbook PDF when exposed in metadata:

```bash
libretexts pdf chem-38132 --out Chemistry.pdf
```

## Identifier Conventions

All commands accept:
1. **Commons Book IDs**: Format `<library>-<id>` (e.g., `chem-38132`, `math-1025`).
2. **Full URLs**: Any valid LibreTexts URL (e.g., `https://chem.libretexts.org/...`).
3. **Bare Deki Page IDs**: Numeric IDs require the `--library <name>` flag (e.g., `38132 --library chem`).

## Downstream Integration Tips

- **Canvas Course Integration**: When converting LibreTexts chapters into Canvas LMS pages with `canvas-pp-cli`, use `--format html` to preserve MathJax markup, diagrams, and formatting.
- **Machine Processing**: Add `--json` to `search`, `page`, `tree`, and `libraries` for structured agent processing.

## Agent Discovery and Errors

Discover the versioned interface without network or file writes:

```bash
libretexts describe --json
```

The JSON manifest has `schema_version: "1"` and describes every command,
including options, output modes, network use, and file-writing behavior.

For machine-readable failures, add `--json-errors`. A numeric page ID without
a library exits 1 and emits this standard-error JSON object:

```bash
libretexts --json-errors page 38132
# stderr: {"schema_version":"1","ok":false,"error":{"code":"LIBRARY_REQUIRED","message":"--library is required with numeric page IDs","hint":"Retry with --library followed by a LibreTexts library name, such as chem.","retryable":false}}
```

Bound a network page read and receive a JSON `meta`/`content` response. Here
`content.text` is capped at 6,000 Unicode characters and `content.html` is
empty because text was selected:

```bash
libretexts page chem-38132 --json --content text --max-chars 6000
```
