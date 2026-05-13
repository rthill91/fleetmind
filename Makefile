SHELL := /usr/bin/env bash
BIN   := bin/fleetmind
PKG   := ./...
GOFLAGS ?=

.PHONY: all build test lint fmt tidy snap clean help

all: build

build:
	@mkdir -p bin
	go build $(GOFLAGS) -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/fleetmind

test:
	go test $(GOFLAGS) -race -count=1 $(PKG)

lint:
	golangci-lint run $(PKG)

fmt:
	gofumpt -l -w .
	goimports -w -local github.com/gjolly/fleetmind .

tidy:
	go mod tidy

snap:
	snapcraft pack

clean:
	rm -rf bin parts stage prime *.snap

help:
	@awk 'BEGIN{FS=":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
