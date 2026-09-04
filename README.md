# libretexts-cli

Agent-friendly CLI for searching LibreTexts libraries and extracting textbook content.

## Install

```bash
go install github.com/johnnylibretexts/libretexts-cli/cmd/libretexts@latest
```

## Build

```bash
go build -o libretexts ./cmd/libretexts
```

This builds `./libretexts` in the source checkout. It does not install the
binary. If an installed `libretexts` is on `PATH`, the commands below may be
used without the `./` prefix; otherwise use `./libretexts` after building.

The API contracts live in `openapi/commons.yaml` and `openapi/deki.yaml`. To generate and validate the raw endpoint CLI with Printing Press:

```bash
cli-printing-press generate \
  --spec openapi/commons.yaml \
  --spec openapi/deki.yaml \
  --name libretexts-api \
  --output /tmp/libretexts-api \
  --spec-source official \
  --validate
```

The compact `libretexts` binary in this repository adds the textbook-level `tree`, `extract`, and `pdf` workflows on top of those API contracts.

The Commons catalog, Deki tree traversal, and fallback behavior were cross-checked against the [LibreTexts Reader importer](https://github.com/johnnylibretexts/libretexts-reader).

## Examples

```bash
./libretexts libraries
./libretexts search "general chemistry" --library chem --limit 5
./libretexts page chem-38132 --json
./libretexts tree chem-38132 --max-pages 30
./libretexts extract chem-38132 --out ./chemistry-1e --format html
./libretexts pdf chem-38132 --out Chemistry_1e.pdf
```

## Agent and MCP interfaces

Discover the current, versioned command manifest without accessing the
network or writing files:

```bash
libretexts describe --json
```

It emits JSON with `schema_version: "1"`, the global `--json-errors` option,
and every command's synopsis, options, output modes, network use, and
file-writing behavior. For example, the manifest reports `describe` as a
network-free, non-writing command and `serve` as an MCP command.

Use `--json-errors` when an automation needs exactly one error object on
standard error. A bare numeric page ID intentionally fails because it has no
library host:

```bash
libretexts --json-errors page 38132
```

The command exits 1 and writes this JSON error to standard error:

```json
{"schema_version":"1","ok":false,"error":{"code":"LIBRARY_REQUIRED","message":"--library is required with numeric page IDs","hint":"Retry with --library followed by a LibreTexts library name, such as chem.","retryable":false}}
```

For bounded page retrieval, request a selected representation and maximum
number of Unicode characters. This networked command returns JSON containing
`meta` and `content`; with `--content text`, `content.html` is empty and the
text is capped at 6,000 characters:

```bash
libretexts page chem-38132 --json --content text --max-chars 6000
```

Start the local MCP server over stdio with its read-only tool set:

```bash
libretexts serve --mcp
```

That server exposes four read-only tools: `libretexts_list_libraries`,
`libretexts_search_books`, `libretexts_get_page`, and
`libretexts_get_tree`. It does not expose extract or PDF-download tools.

To opt into file-writing tools, choose a directory that the server may use:

```bash
libretexts serve --mcp --write-root ./libretexts-output
```

With `--write-root`, the MCP server additionally exposes
`libretexts_extract_book` and `libretexts_download_pdf`. Their output paths
are relative to, and confined beneath, that root. Both MCP modes may make
network requests to LibreTexts when a tool is called; starting the server does
not itself fetch content.

The write-root checks reject absolute output paths, lexical traversal, and
existing symbolic links in requested output paths. This is a pathname safety
boundary, not a hardened concurrent-filesystem guarantee: writes use ordinary
path operations rather than dirfd/no-follow operations, so a concurrent
replacement after validation is not prevented. Existing regular
files may be replaced, and replacement is not guaranteed to be atomic.

An MCP host configuration should use the absolute path of an already installed
binary. For example, this is an installation-time configuration, separate from
building this source checkout (and does not assert that this path exists here):

```json
{
  "mcpServers": {
    "libretexts": {
      "command": "/opt/homebrew/bin/libretexts",
      "args": ["serve", "--mcp"]
    }
  }
}
```

## Verification

Run the repeatable local verification target before relying on a built binary:

```bash
make verify
```

It checks Go formatting, runs `go vet` with and without the `integration`
build tag, runs the unit suite under the race detector without test-cache
reuse, builds `./libretexts`, and then runs the live integration suite. It
does not install, publish, or release anything.

`make verify` needs network access because the integration suite exercises the
real Commons, Deki, and download services. That is deliberate: an upstream
endpoint that has moved or gone dead should fail the build rather than ship.
To skip the networked half:

```bash
make verify-offline   # formatting, vet, race-enabled unit tests, build
make test-integration # live services only
```

The integration tests are behind a `//go:build integration` tag, so a plain
`go test ./...` stays offline and fast.

## Commands

- `libraries`: list known LibreTexts library hosts.
- `search`: search actual textbooks through the LibreTexts Commons catalog.
- `page`: fetch page metadata and content.
- `tree`: list the complete textbook hierarchy using the Deki tree API, with a recursive subpage fallback.
- `extract`: extract every page to Markdown, HTML, or JSON files and write an `index.json` manifest.
- `pdf`: download a book or section PDF from the LibreTexts download service.

Search returns Commons book IDs such as `chem-38132`. Those IDs and normal LibreTexts page URLs can be used directly by every content command. For a bare numeric Deki page ID, also provide `--library` so the CLI knows which host to call.

Use `--format html` or `--format json` when the original page markup, links, images, tables, and math must be retained. Markdown extraction is a normalized plain-text reading copy. Use `--max-pages N` for bounded runs; omit it to traverse the full textbook.

## API endpoints

- `https://commons.libretexts.org/api/v1/search/books-v2`
- `https://{library}.libretexts.org/@api/deki/pages/{page-id}/tree`
- `https://{library}.libretexts.org/@api/deki/pages/{page-id}/contents`
- `https://downloads.libretexts.org/api/v1/download/{library}-{page-id}/pdf`
- `https://{library}.libretexts.org/@api/deki/pages/{page-id}/pdf/...` (fallback)

PDFs come from the Commons download service. The `contents.alt` URL that Deki
page metadata advertises is tried only as a fallback, because it has been
returning HTTP 500. LibreTexts renders PDFs for books and major sections, not
for every leaf page; a page with no rendered PDF reports `PDF_UNAVAILABLE`.

## License

MIT. See [LICENSE](LICENSE).

Note that the license covers this CLI only. Textbook content retrieved from
LibreTexts carries its own per-book license, reported in the `license` field of
search results.
