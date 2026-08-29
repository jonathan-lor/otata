# otata: development tasks.
BINARY  := otata
MODULE  := github.com/jonathan-lor/otata
BIN_DIR := $(HOME)/.local/bin

.PHONY: build install test vet fmt

build: ## Compile for this machine into bin/
	go build -o bin/$(BINARY) .

## install copies rather than symlinks on purpose: launchd cannot execute a
## binary inside a TCC-protected directory such as ~/Documents, and it fails by
## hanging in dyld rather than erroring, which is very hard to diagnose.
## Write beside the target and rename into place. Overwriting a binary that is
## currently running corrupts its mapped image and macOS kills the process with
## SIGKILL; rename swaps the directory entry and leaves the running inode alone.
install: build
	@mkdir -p $(BIN_DIR)
	cp bin/$(BINARY) $(BIN_DIR)/.$(BINARY).new
	@chmod +x $(BIN_DIR)/.$(BINARY).new
	mv $(BIN_DIR)/.$(BINARY).new $(BIN_DIR)/$(BINARY)
	@echo "installed $(BIN_DIR)/$(BINARY)"
	@if [ -f "$(HOME)/Library/LaunchAgents/com.anakepha.otata.plist" ]; then \
		echo "refreshing the launch agent so it runs the new binary"; \
		$(BIN_DIR)/$(BINARY) autostart on >/dev/null 2>&1 || true; \
	fi

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
