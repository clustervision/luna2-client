SHELL := /bin/bash

GO ?= go
GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK ?= govulncheck

BIN_DIR := bin
CMDS := lpart-storage-prepare lpart-storage-check lpart-osimage-install lpart-bootloader-finalise lpart-node-installer lpart-emit lpart-tui

.PHONY: all fmt vet test test-race lint vuln 95luna-sync handover-flush check build build-linux clean

all: check build-linux

fmt:
	$(GOFMT) -w .

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

lint:
	$(GOLANGCI_LINT) run

vuln:
	$(GOVULNCHECK) ./...

# The three per-family 95luna dracut trees must stay byte-identical (one
# module, three packaging copies). See scripts/dev/check-95luna-sync.sh.
95luna-sync:
	bash scripts/dev/check-95luna-sync.sh

# every installer hand-over flushes the installed disk first. See scripts/dev/check-handover-flush.sh.
handover-flush:
	bash scripts/dev/check-handover-flush.sh

# No version-gated external-tool flags in production code (FTA BE-P1/CCF-9:
# lsblk PATH needs util-linux >= 2.33, el8 ships 2.32 -- bit twice).
tool-portability:
	bash scripts/dev/check-version-gated-tools.sh

check: fmt vet test lint vuln 95luna-sync handover-flush tool-portability

build:
	$(GO) build ./...

# Default host build of every binary (linux/amd64 controller assumed)
build-host: $(addprefix host-,$(CMDS))
host-%:
	$(GO) build -o $(BIN_DIR)/$* ./cmd/$*

# Cross-compiled static linux/amd64 + linux/arm64 builds for the installer toolbox
build-linux: $(addprefix linux-amd64-,$(CMDS)) $(addprefix linux-arm64-,$(CMDS)) $(addprefix linux-riscv64-,$(CMDS))

linux-amd64-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w" \
		-o $(BIN_DIR)/$*-linux-amd64 ./cmd/$*

linux-arm64-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" \
		-o $(BIN_DIR)/$*-linux-arm64 ./cmd/$*

linux-riscv64-%:
	CGO_ENABLED=0 GOOS=linux GOARCH=riscv64 $(GO) build -trimpath -ldflags="-s -w" \
		-o $(BIN_DIR)/$*-linux-riscv64 ./cmd/$*


clean:
	rm -rf $(BIN_DIR)
