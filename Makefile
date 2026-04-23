.PHONY: build test lint install

VERSION ?= dev
LDFLAGS = -ldflags "-X github.com/blackbuck/ec2ctl/commands.Version=$(VERSION)"
INSTALL_DIR ?= /usr/local/bin

build:
	go build $(LDFLAGS) -o bin/bbctl ./cmd/bbctl

install: build
	cp bin/bbctl $(INSTALL_DIR)/bbctl
	@echo "Installed to $(INSTALL_DIR)/bbctl"

test:
	go test ./... -race -timeout 60s

lint:
	golangci-lint run ./...
