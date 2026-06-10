SHELL := /bin/bash

GO ?= go
NODE ?= node
BINARY ?= ./thoughtflow
GO_PACKAGES := ./...
WEB_DIR := internal/modules/application/thoughtflow/service/web
GO_FILES := $(shell find cmd internal -name '*.go' -type f)

.PHONY: help fmt fmt-check test test-duckdb build node-check node-test browser-test check clean

help:
	@printf '%s\n' \
		'Targets:' \
		'  fmt           Format Go files under cmd/ and internal/' \
		'  fmt-check     Verify Go formatting without changing files' \
		'  test          Run default Go tests' \
		'  test-duckdb   Run Go tests with the duckdb build tag' \
		'  build         Build the thoughtflow binary' \
		'  node-check    Run JavaScript syntax checks' \
		'  node-test     Run Node component tests' \
		'  browser-test  Run embedded UI browser smoke tests' \
		'  check         Run the full validation matrix' \
		'  clean         Remove local build artifacts'

fmt:
	$(GO)fmt -w $(GO_FILES)

fmt-check:
	@test -z "$$($(GO)fmt -l $(GO_FILES))"

test:
	$(GO) test $(GO_PACKAGES)

test-duckdb:
	CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GO) test -tags duckdb $(GO_PACKAGES)

build:
	$(GO) build -o $(BINARY) ./cmd/thoughtflow

node-check:
	$(NODE) --check $(WEB_DIR)/vendor/markdown-it.min.js
	$(NODE) --check $(WEB_DIR)/app.js

node-test:
	$(NODE) --test $(WEB_DIR)/app.test.js

browser-test:
	$(NODE) --test $(WEB_DIR)/app.browser.test.js

check: fmt-check test build test-duckdb node-check node-test browser-test

clean:
	rm -f $(BINARY)
