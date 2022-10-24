###############################################################################
# VARIABLES
###############################################################################

export GO111MODULE := on
export GOPROXY = https://proxy.golang.org,direct

PROJECT_NAME=mimikry
MAIN_FILE=./cmd/mimikry/main.go

PKG_PATH=github.com/nikoksr/$(PROJECT_NAME)
BUILD_DEBUG_DIR=./bin/debug/
BUILD_RELEASE_DIR=./bin/release/

GIT_TAG=$(shell git describe --tags --abbrev=0 --dirty=+CHANGES)
GIT_REV=$(shell git rev-parse --short HEAD)

###############################################################################
# DEPENDENCIES
###############################################################################

setup:
	go mod tidy
	@go install mvdan.cc/gofumpt@latest
	@go install github.com/daixiang0/gci@latest
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
.PHONY: setup

###############################################################################
# TESTS
###############################################################################

test:
	go test -failfast -race ./...
.PHONY: test

gen-coverage:
	@go test -race -covermode=atomic -coverprofile=coverage.out ./... > /dev/null
.PHONY: gen-coverage

coverage: gen-coverage
	go tool cover -func coverage.out
.PHONY: coverage

coverage-html: gen-coverage
	go tool cover -html=coverage.out -o cover.html
.PHONY: coverage-html

###############################################################################
# CODE HEALTH
###############################################################################

fmt:
	@gofumpt -w -l . > /dev/null
	@goimports -w -l -local $(PKG_PATH) . > /dev/null
	@gci write --section standard --section default --section "Prefix($(PKG_PATH))" . > /dev/null
.PHONY: fmt

lint:
	@golangci-lint run --config .golangci.yml
.PHONY: lint

ci: fmt test lint
.PHONY: ci

###############################################################################
# BUILDS
###############################################################################

prepare-build:
	@mkdir -p $(BUILD_DEBUG_DIR) $(BUILD_RELEASE_DIR)
.PHONY: prepare-build

check-optimizations: prepare-build
	go build -gcflags='-m -m' -o $(BUILD_DEBUG_DIR)$(PROJECT_NAME) $(MAIN_FILE)
.PHONY: check-optimizations

build-debug: prepare-build
	CGO_ENABLED=0 go build -o $(BUILD_DEBUG_DIR)$(PROJECT_NAME) $(MAIN_FILE) > /dev/null
.PHONY: build-debug

build-release: prepare-build
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BUILD_RELEASE_DIR)$(PROJECT_NAME) $(MAIN_FILE) > /dev/null
.PHONY: build-release

dev: build-debug
	@$(BUILD_DEBUG_DIR)$(PROJECT_NAME)
.PHONY: dev

install:
	CGO_ENABLED=0 go install $(MAIN_FILE)
.PHONY: install

clean:
	rm -rf ./bin 2> /dev/null
	rm cover.html coverage.out 2> /dev/null
.PHONY: clean

.DEFAULT_GOAL := build-debug
