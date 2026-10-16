# otata: development tasks.
BINARY  := otata
BIN_DIR := $(HOME)/.local/bin

.PHONY: build install test vet fmt

build: ## Compile for this machine into bin/
	go build -o bin/$(BINARY) .

## install copies rather than symlinks, since launchd hangs on a binary in a
## TCC-protected directory like ~/Documents. It renames into place, because
## overwriting a running binary kills it on macOS and fails on Linux. Then it
## refreshes the launch agent or systemd unit so the new binary runs.
install: build
	@mkdir -p $(BIN_DIR)
	cp bin/$(BINARY) $(BIN_DIR)/.$(BINARY).new
	@chmod +x $(BIN_DIR)/.$(BINARY).new
	mv $(BIN_DIR)/.$(BINARY).new $(BIN_DIR)/$(BINARY)
	@echo "installed $(BIN_DIR)/$(BINARY)"
	@if [ -f "$(HOME)/Library/LaunchAgents/com.anakepha.otata.plist" ] || \
	   [ -f "$${XDG_CONFIG_HOME:-$(HOME)/.config}/systemd/user/otata.service" ]; then \
		echo "refreshing the service so it runs the new binary"; \
		$(BIN_DIR)/$(BINARY) autostart on >/dev/null 2>&1 || true; \
	fi

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
