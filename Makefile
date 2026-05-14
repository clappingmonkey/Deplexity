BINARY_NAME=deplexity
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

.PHONY: build install clean run test lint tidy

build:
	go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/deplexity

install:
	go install $(LDFLAGS) ./cmd/deplexity

clean:
	rm -rf bin/
	go clean

run: build
	./bin/$(BINARY_NAME)

test:
	go test -v ./...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

.DEFAULT_GOAL := build
