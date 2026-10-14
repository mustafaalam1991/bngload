# bngload Phase 0 — build, test and install.
#
#   make                 build bin/bngload and engines/afprobe (release)
#   make test            unit tests: Go, Rust, Python (no root needed)
#   sudo make test-integration   veth-based tests (root; creates its own veth pairs)
#   make lint            go vet, cargo clippy, shellcheck, pyflakes (if installed)
#   sudo make install    /usr/local/bin/{bngload,afprobe,bngload-lab}
#   make clean

PREFIX   ?= /usr/local
BINDIR   ?= $(PREFIX)/bin
LIBDIR   ?= $(PREFIX)/lib/bngload
GO       ?= go
CARGO    ?= cargo
PYTHON   ?= python3

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-phase0)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VPKG     := github.com/mustafaalam1991/bngload/internal/version
LDFLAGS  := -s -w -X $(VPKG).Version=$(VERSION) -X $(VPKG).Commit=$(COMMIT) -X $(VPKG).BuildDate=$(DATE)

AFPROBE_DIR := engines/afprobe
AFPROBE_BIN := $(AFPROBE_DIR)/target/release/afprobe
LAB_DIR     := tools/lab

.PHONY: all build bngload afprobe test test-go test-rust test-python test-integration lint install uninstall clean

all: build
build: bngload afprobe

bngload:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/bngload ./cmd/bngload

afprobe:
	cd $(AFPROBE_DIR) && AFPROBE_COMMIT=$(COMMIT) $(CARGO) build --release --locked

test: test-go test-rust test-python

test-go:
	$(GO) vet ./...
	$(GO) test ./...

test-rust:
	cd $(AFPROBE_DIR) && $(CARGO) test --locked

test-python:
	cd $(LAB_DIR) && $(PYTHON) -m pytest

# Needs root. Every test creates and removes its own veth pairs (bit*, afp*)
# or uses blt0/blt1 from scripts/lab-veth.sh for the BNG Blaster self-loop.
test-integration:
	$(GO) test -tags integration -count=1 ./test/integration/
	cd $(AFPROBE_DIR) && $(CARGO) test --release --locked --test veth -- --ignored
	cd $(LAB_DIR) && $(PYTHON) -m pytest -m blaster

lint:
	$(GO) vet ./...
	cd $(AFPROBE_DIR) && $(CARGO) clippy --all-targets --locked -- -D warnings
	shellcheck -S warning scripts/*.sh
	-cd $(LAB_DIR) && $(PYTHON) -m pyflakes src tests

# The Python harness is installed as plain files plus a wrapper (no pip, no
# network): it only needs python3 (and python3-tomli on Ubuntu 22.04).
install: build
	install -D -m 0755 bin/bngload $(DESTDIR)$(BINDIR)/bngload
	install -D -m 0755 $(AFPROBE_BIN) $(DESTDIR)$(BINDIR)/afprobe
	rm -rf $(DESTDIR)$(LIBDIR)/python
	install -d $(DESTDIR)$(LIBDIR)/python
	cp -r $(LAB_DIR)/src/bngload_lab $(DESTDIR)$(LIBDIR)/python/
	find $(DESTDIR)$(LIBDIR)/python -name __pycache__ -prune -exec rm -rf {} +
	printf '#!/bin/sh\nPYTHONPATH=%s/python exec %s -m bngload_lab "$$@"\n' \
		'$(LIBDIR)' '$(shell command -v $(PYTHON) || echo /usr/bin/python3)' > $(DESTDIR)$(BINDIR)/bngload-lab
	chmod 0755 $(DESTDIR)$(BINDIR)/bngload-lab
	@echo "installed bngload, afprobe and bngload-lab in $(DESTDIR)$(BINDIR)"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/bngload $(DESTDIR)$(BINDIR)/afprobe $(DESTDIR)$(BINDIR)/bngload-lab
	rm -rf $(DESTDIR)$(LIBDIR)

clean:
	rm -rf bin
	cd $(AFPROBE_DIR) && $(CARGO) clean
	find . -name __pycache__ -prune -exec rm -rf {} +
	rm -rf $(LAB_DIR)/.pytest_cache $(LAB_DIR)/src/*.egg-info
