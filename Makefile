BINARY := opencode-phone-mode
PREFIX ?= $(HOME)/.local
USER ?= $(shell id -un)
CONFIG_DIR ?= $(HOME)/.config/opencode-phone-mode
GO ?= go

IDLE_SERVICE := opencode-phone-mode-idle.service
IDLE_TIMER := opencode-phone-mode-idle.timer

.PHONY: build test check install install-idle-timer clean

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

# The timer runs as root so CPU writes need no sudo password. Run this target
# from your own shell: sudo will prompt for your password once.
install-idle-timer: build
	install -Dm755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	sed -e 's|@HOME@|$(HOME)|g' -e 's|@USER@|$(USER)|g' systemd/$(IDLE_SERVICE) | sudo tee /etc/systemd/system/$(IDLE_SERVICE) >/dev/null
	sed 's|@HOME@|$(HOME)|g' systemd/$(IDLE_TIMER) | sudo tee /etc/systemd/system/$(IDLE_TIMER) >/dev/null
	sudo systemctl daemon-reload
	sudo systemctl enable --now $(IDLE_TIMER)

clean:
	rm -f $(BINARY)
