.PHONY: all build test run-tests lint clean coverage examples examples-clean examples-test utilities utilities-clean install uninstall check-docs hil-test api-docs help

# The single `gogl` binary. The four earlier utilities became importable packages under
# utilities/internal/ -- reservations, netcfg, clients, profile -- so their logic and
# tests survive the move to one command tree. See docs/DESIGN-V2.md.
#
UTILITIES := gogl

# All examples
EXAMPLES := basic list reservations

# Install destination. ~/.local/bin per the XDG user-directory convention: it is on
# PATH by default via systemd and Debian's .profile, and nothing this tool does needs
# root. Override with: make install INSTALL_DIR=/somewhere/else
INSTALL_DIR ?= $(HOME)/.local/bin

# Earlier releases installed to ~/bin. `make uninstall` clears both, because a stale
# binary shadowing a fresh one cost real debugging time twice.
LEGACY_INSTALL_DIR := $(HOME)/bin

# Build stamp. Prefers a git tag, falls back to a short revision, then to nothing --
# in which case the binary reports the toolchain's own VCS stamp.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -X github.com/gherlein/gogl/utilities/internal/conn.Version=$(VERSION)

.DEFAULT_GOAL := help

all: lint test build

# Build the module (compile-checks the whole tree) and the runnable utilities
# into bin/. Nothing is ever written to the repo root.
build: utilities
	go build ./...

test:
	go test -v -race -cover ./...

# Alias for `test`, for callers that use the run-tests convention.
run-tests: test

lint:
	golangci-lint run ./...

clean: examples-clean utilities-clean
	go clean ./...
	rm -rf coverage.out coverage.html

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# === Examples ===

examples:
	@mkdir -p bin
	@for ex in $(EXAMPLES); do \
		echo "Building $$ex..."; \
		go build -o bin/$$ex ./examples/$$ex; \
	done
	@echo "All examples built in bin/"

examples-clean:
	rm -rf bin/

examples-test:
	@echo "Testing examples compile..."
	@for ex in $(EXAMPLES); do \
		echo "  Checking $$ex..."; \
		go build -o /dev/null ./examples/$$ex || exit 1; \
	done
	@echo "All examples compile successfully."

# === Utilities ===

utilities:
	@mkdir -p bin
	@if [ -z "$(UTILITIES)" ]; then \
		echo "No utilities to build: the command tree is being rebuilt as a single"; \
		echo "'gogl' binary. See docs/DESIGN-V2.md. The library and the four packages"; \
		echo "under utilities/internal/ are built and tested by 'make test'."; \
	else \
		for util in $(UTILITIES); do \
			echo "Building $$util..."; \
			go build -ldflags "$(LDFLAGS)" -o bin/$$util ./utilities/$$util; \
		done; \
		echo "All utilities built in bin/"; \
	fi

utilities-clean:
	rm -rf bin/

install: utilities
	@mkdir -p $(INSTALL_DIR)
	@for util in $(UTILITIES); do \
		echo "Installing $$util to $(INSTALL_DIR)/$$util"; \
		install -m 755 bin/$$util $(INSTALL_DIR)/$$util; \
	done
	@echo "All utilities installed to $(INSTALL_DIR)."
	@case ":$$PATH:" in *":$(INSTALL_DIR):"*) ;; *) echo "Note: $(INSTALL_DIR) is not on your PATH.";; esac

# Regenerate docs/api/ from GL.iNet's API description. The description is fetched
# to /tmp rather than vendored: it comes from a GPL-3.0 package and this project is
# MIT.
# Remove installed binaries from both the current and the legacy location. The legacy
# sweep matters: a stale ~/bin/goglps shadowed a fresh build twice, and each time the
# symptom was a flag that "did not exist".
uninstall:
	@for util in $(UTILITIES) gogl; do \
		for dir in $(INSTALL_DIR) $(LEGACY_INSTALL_DIR); do \
			if [ -e "$$dir/$$util" ]; then \
				echo "Removing $$dir/$$util"; \
				rm -f "$$dir/$$util"; \
			fi; \
		done; \
	done
	@echo "Uninstalled."

# Check the documentation against the binaries rather than against memory.
#
# Two classes of drift this catches, both of which happened: a flag documented that does
# not exist (or exists and is undocumented), and a Go snippet in README.md that no longer
# compiles after a signature change.
check-docs: utilities
	@scripts/check-docs.sh

# Hardware-in-the-loop test. Writes to a real router and restores what it found.
#
# Not part of `make test`, and never will be: `make test` runs against an in-process mock
# with no hardware, and mixing the two would make a green suite mean two different things.
hil-test: install
	@scripts/hil-test.sh $(HIL_ARGS)

api-docs:
	./scripts/fetch-api-description.sh /tmp/gl-api-description.json
	python3 scripts/generate-api-docs.py /tmp/gl-api-description.json

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Main targets:"
	@echo "  all           Run lint, test, and build"
	@echo "  build         Build the module and utilities into bin/"
	@echo "  test          Run all tests"
	@echo "  run-tests     Alias for test"
	@echo "  lint          Run linter"
	@echo "  clean         Clean all build artifacts"
	@echo "  coverage      Generate coverage report"
	@echo ""
	@echo "Example targets:"
	@echo "  examples        Build all examples to bin/"
	@echo "  examples-clean  Remove example binaries"
	@echo "  examples-test   Verify all examples compile"
	@echo ""
	@echo "Utility targets:"
	@echo "  utilities       Build all utilities to bin/"
	@echo "  utilities-clean Remove utility binaries"
	@echo "  install         Build and install to ~/.local/bin (override INSTALL_DIR)"
	@echo "  uninstall       Remove installed binaries, including from the legacy ~/bin"
	@echo ""
	@echo "Documentation targets:"
	@echo "  check-docs      Verify README flags and links against the built binaries"
	@echo "  hil-test        Run the hardware-in-the-loop test (writes to a real router)"
	@echo "  api-docs        Regenerate docs/api/ from GL.iNet's API description"
