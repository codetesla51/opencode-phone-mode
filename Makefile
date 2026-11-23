BINARY := opencode-phone-mode
PREFIX ?= $(HOME)/.local
CONFIG_DIR ?= $(HOME)/.config/opencode-phone-mode
GO ?= go

.PHONY: build test check install clean

build:
	$(GO) build -trimpath -o $(BINARY) .

test:
	$(GO) test -race -count=1 ./...

check:
	@files=$$(gofmt -l .); if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi
	$(GO) vet ./...
	$(GO) test -race -count=1 ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; fi

install: build
	install -Dm755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@if [ ! -f "$(CONFIG_DIR)/config.toml" ]; then \
		install -Dm644 config.example.toml "$(CONFIG_DIR)/config.toml"; \
		echo "installed default config to $(CONFIG_DIR)/config.toml"; \
	fi

clean:
	rm -f $(BINARY)
