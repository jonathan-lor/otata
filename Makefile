# otata: development tasks.
BINARY  := otata
MODULE  := github.com/jonathan-lor/otata
BIN_DIR := $(HOME)/.local/bin

.PHONY: build install test vet fmt

build: ## Compile for this machine into bin/
	go build -o bin/$(BINARY) .

## install copies instead of symlinking on purpose. launchd can't run a binary
## in a TCC-protected directory like ~/Documents, and it fails by hanging in
## dyld instead of erroring, which is very hard to diagnose.
## Write beside the target and rename into place. Overwriting a currently running
## binary corrupts its mapped image and macOS kills the process with SIGKILL;
## Linux refuses the write outright (ETXTBSY). Rename swaps the directory
## entry and leaves the running inode alone.
## A unit that keeps the server alive, launchd's or systemd's, is then
## refreshed: 'autostart on' reinstalls and restarts it, so the server that
## returns is the new binary and not the old image still running.
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
