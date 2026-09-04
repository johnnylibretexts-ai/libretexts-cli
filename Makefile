.PHONY: build test test-integration install clean verify verify-offline

BIN_DIR ?= $(HOME)/.local/bin
ALT_BIN_DIR ?= /opt/homebrew/bin

build:
	go build -o libretexts ./cmd/libretexts

test:
	go test -v ./...

# Runs against the live LibreTexts services, so an upstream contract that has
# moved or gone dead fails here instead of shipping.
test-integration:
	go test ./... -tags=integration -count=1

# Full gate, including the live integration suite. Requires network access.
verify: verify-offline test-integration

# Everything that runs without network access.
verify-offline:
	@unformatted="$$(gofmt -l cmd/libretexts)"; \
	if [ -n "$$unformatted" ]; then printf '%s\n' "$$unformatted"; exit 1; fi
	go vet ./...
	go vet -tags=integration ./...
	go test ./... -race -count=1
	go build -o libretexts ./cmd/libretexts

install: build
	mkdir -p $(BIN_DIR)
	install -m 755 libretexts $(BIN_DIR)/libretexts
	ln -sf $(BIN_DIR)/libretexts $(BIN_DIR)/libretexts-cli
	@if [ -d $(ALT_BIN_DIR) ] && [ -w $(ALT_BIN_DIR) ]; then \
		install -m 755 libretexts $(ALT_BIN_DIR)/libretexts; \
		ln -sf $(ALT_BIN_DIR)/libretexts $(ALT_BIN_DIR)/libretexts-cli; \
	fi

clean:
	rm -f libretexts
