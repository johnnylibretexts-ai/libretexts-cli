# libretexts-cli

[![CI](https://github.com/johnnylibretexts/libretexts-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/johnnylibretexts/libretexts-cli/actions/workflows/ci.yml)

Search and extract open textbook content from [LibreTexts](https://libretexts.org)
from the command line.

LibreTexts is one of the largest open educational resource projects — free,
openly licensed textbooks across chemistry, biology, mathematics, engineering,
and a dozen other subjects, organized into subject libraries like `chem` and
`bio`. Its content lives behind two separate web APIs and is normally read one
page at a time in a browser.

This tool turns that into something you can script: find a textbook, walk its
chapter hierarchy, pull page text or the original HTML, extract an entire book
to files, or download the rendered PDF.

It is built to be driven by AI agents as well as people. Every command speaks
JSON, errors arrive as a single structured object that says whether retrying
will help, and long pages can be read in bounded windows that fit a model's
context. There are no dependencies outside the Go standard library.

**Use it to:** feed textbook content to an agent or RAG pipeline · bulk-export a
book for a course · pull chapter HTML with intact MathJax, tables, and figures ·
grab the official PDF.

## Install

```bash
go install github.com/johnnylibretexts/libretexts-cli/cmd/libretexts@latest
```

Requires Go 1.26.4 or newer. To build from a checkout instead:

```bash
go build -o libretexts ./cmd/libretexts
```

That produces `./libretexts` in the checkout without installing it. Examples
below assume the binary is on your `PATH`.

## Quick start

Find a textbook. Search returns a book ID like `chem-285427` that every other
command accepts:

```console
$ libretexts search "organic chemistry" --library chem --limit 2
 1. [chem] Complex Molecular Synthesis
    https://chem.libretexts.org/Bookshelves/Organic_Chemistry/Complex_Molecular_Synthesis_(Salomon)
    id=chem-285427 author=Robert G. Salomon
    Design and Logic in the Biosynthesis and Total Synthesis of Natural Products
 2. [chem] How to be a Successful Organic Chemist
    https://chem.libretexts.org/Bookshelves/Organic_Chemistry/Book%3A_How_to_be_a_Successful_Organic_Chemist_(Sandtorv)
    id=chem-135934 author=Alexander Sandtorv
    How to be a successful organic chemist is meant as an introductory text...
```

See how a book is organized:

```console
$ libretexts tree chem-21927 --max-pages 5
- Basic Principles of Organic Chemistry (Roberts and Caserio) (21927)
  - Front Matter (182332)
    - TitlePage (189162)
    - InfoPage (189163)
    - Table of Contents (182339)
```

Read a page, or export the whole book:

```bash
libretexts page chem-21927                            # readable text
libretexts page chem-21927 --html                     # original markup
libretexts extract chem-21927 --out ./organic-chem    # every page to files
libretexts pdf chem-21927 --out organic-chem.pdf      # rendered PDF
```

## Identifiers

Every content command takes a book ID, a LibreTexts URL, or a numeric page ID:

```bash
libretexts page chem-21927
libretexts page "https://chem.libretexts.org/Bookshelves/Organic_Chemistry/..."
libretexts page 21927 --library chem
```

A bare numeric ID needs `--library`, because page IDs are only unique within a
library. `libretexts libraries` lists all 14.

## Commands

| Command | What it does |
|---|---|
| `libraries` | List the LibreTexts subject libraries. |
| `search QUERY` | Search the Commons textbook catalog. `--library` to filter, `--limit` to bound. |
| `page ID` | Fetch one page's metadata and content. |
| `tree ID` | Print the full chapter and page hierarchy. `--max-pages` to bound. |
| `extract ID --out DIR` | Write every page to files plus an `index.json` manifest. |
| `pdf ID --out FILE` | Download the rendered PDF. |
| `describe` | Print the machine-readable capability manifest. |

`extract --format` takes `markdown` (default), `html`, or `json`. Use `html` or
`json` when the original markup — links, images, tables, MathJax — has to
survive; markdown is a normalized plain-text reading copy.

`--max-pages N` bounds `tree` and `extract`; omit it to traverse the whole book.
`extract --delay` (default `100ms`) paces requests between pages.

PDFs are rendered by LibreTexts for books and major sections, not for every leaf
page. A page with no PDF reports `PDF_UNAVAILABLE`.

## Agent interface

Discover the full command surface without touching the network:

```bash
libretexts describe --json
```

This emits a versioned manifest — `schema_version`, and for every command its
synopsis, options with defaults and allowed values, output modes, whether it
uses the network, and whether it writes files. An agent can read it instead of
guessing at flags.

Add `--json` to `search`, `page`, or `tree` for structured output:

```console
$ libretexts page chem-21927 --json --content metadata
{
  "meta": {
    "id": "21927",
    "title": "Basic Principles of Organic Chemistry (Roberts and Caserio)",
    "uri": "https://chem.libretexts.org/Bookshelves/Organic_Chemistry/...",
    "modified": "Tue, 07 Mar 2023 23:48:38 GMT",
    "pdf": "https://chem.libretexts.org/@api/deki/pages/21927/pdf/...",
    "has_subpages": true,
    "library": "chem"
  }
}
```

`--content` selects the representation: `metadata`, `text`, `html`, or `both`.

### Reading long pages in windows

`--max-chars` and `--offset` bound content by Unicode character, so a page
larger than a model's context can be read across several calls. The response
carries a `window` telling you where you are and where to resume:

```console
$ libretexts page chem-21927 --json --content text --max-chars 120
  "window": {
    "offset": 0,
    "returned_chars": 120,
    "total_chars": 1982,
    "truncated": true,
    "next_offset": 120
  }
```

Feed `next_offset` back as `--offset` until `truncated` is `false`.

### Errors

`--json-errors` writes exactly one JSON object to stderr and exits 1:

```console
$ libretexts --json-errors page 38132
{"schema_version":"1","ok":false,"error":{
  "code":"LIBRARY_REQUIRED",
  "message":"--library is required with numeric page IDs",
  "hint":"Retry with --library followed by a LibreTexts library name, such as chem.",
  "retryable":false}}
```

`retryable` distinguishes a transient upstream failure from a permanent one, so
an agent knows whether backing off will ever help. `hint` says what to do next.
Codes include `LIBRARY_REQUIRED`, `UNKNOWN_LIBRARY`, `INVALID_IDENTIFIER`,
`PDF_UNAVAILABLE`, `UPSTREAM_HTTP_ERROR`, and `NETWORK_ERROR`.

Standard output stays clean: content goes to stdout, diagnostics to stderr.

### As a Claude Code skill

`SKILL.md` in this repository defines the tool as an agent skill, with usage
patterns and worked examples.

## Development

```bash
make verify          # gofmt, vet, race-enabled unit tests, build, live integration tests
make verify-offline  # everything above except the networked tests
make test            # unit tests only
```

`make verify` needs network access: the integration suite runs against the real
Commons, Deki, and download services so an endpoint that has moved or gone dead
fails the build rather than shipping. Those tests sit behind a
`//go:build integration` tag, so a plain `go test ./...` stays offline and fast.
CI runs the offline checks on every push and the live suite nightly.

## API endpoints

```
https://commons.libretexts.org/api/v1/search/books-v2                catalog search
https://{library}.libretexts.org/@api/deki/pages/{id}                page metadata
https://{library}.libretexts.org/@api/deki/pages/{id}/contents       page content
https://{library}.libretexts.org/@api/deki/pages/{id}/tree           hierarchy
https://downloads.libretexts.org/api/v1/download/{library}-{id}/pdf  PDF
```

Contracts for the first two services are in `openapi/commons.yaml` and
`openapi/deki.yaml`. PDFs come from the download service; the `contents.alt`
URL that Deki page metadata advertises is tried only as a fallback, because it
has been returning HTTP 500.

## License

MIT — see [LICENSE](LICENSE).

This covers the CLI itself. Textbook content retrieved from LibreTexts carries
its own per-book license, reported in the `license` field of search results.
