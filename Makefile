SHELL := /bin/bash

GO ?= go
NODE ?= node
BINARY ?= ./thoughtflow
GO_PACKAGES := ./...
WEB_DIR := internal/modules/application/thoughtflow/service/web
GO_FILES := $(shell find cmd internal -name '*.go' -type f)
# libstdc++ is not always installed as a bare symlink (only libstdc++.so.6).
# The DuckDB static library references `-lstdc++` and Go's CGO pipeline
# does not rewrite that flag, so on minimal images we have to provide a
# bare-name symlink in a search path of our own. We stage it under
# $(LIBSTDCPP_STAGE) and add the directory to the linker search path.
LIBSTDCPP_STAGE := /tmp/thoughtflow-libstdcxx
LIBSTDCPP_SYMLINK := $(LIBSTDCPP_STAGE)/libstdc++.so
CGO_LDFLAGS ?= -L$(LIBSTDCPP_STAGE)
# Bootstrap the symlink if it's missing. `touch` is a no-op once the link
# exists, so this is safe to leave in the target.
$(LIBSTDCPP_SYMLINK):
	@mkdir -p $(LIBSTDCPP_STAGE)
	@ln -sf /usr/lib/x86_64-linux-gnu/libstdc++.so.6 $(LIBSTDCPP_SYMLINK)

.PHONY: help fmt fmt-check test test-duckdb build node-check node-test node-test-i18n i18n-check browser-test check clean

help:
	@printf '%s\n' \
		'Targets:' \
		'  fmt              Format Go files under cmd/ and internal/' \
		'  fmt-check        Verify Go formatting without changing files' \
		'  test             Run default Go tests' \
		'  test-duckdb      Run Go tests with the duckdb build tag' \
		'  build            Build the thoughtflow binary' \
		'  node-check       Run JavaScript syntax checks' \
		'  node-test        Run Node component tests' \
		'  node-test-i18n   Run i18n registry tests' \
		'  i18n-check       Verify every i18n key used in app.js/index.html is translated' \
		'  browser-test     Run embedded UI browser smoke tests' \
		'  check            Run the full validation matrix' \
		'  clean            Remove local build artifacts'

fmt:
	$(GO)fmt -w $(GO_FILES)

fmt-check:
	@test -z "$$($(GO)fmt -l $(GO_FILES))"

test:
	$(GO) test $(GO_PACKAGES)

test-duckdb: $(LIBSTDCPP_SYMLINK)
	CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GO) test -tags duckdb $(GO_PACKAGES)

build: $(LIBSTDCPP_SYMLINK)
	CGO_LDFLAGS="$(CGO_LDFLAGS)" $(GO) build -o $(BINARY) ./cmd/thoughtflow

node-check:
	$(NODE) --check $(WEB_DIR)/vendor/markdown-it.min.js
	$(NODE) --check $(WEB_DIR)/i18n/index.js
	$(NODE) --check $(WEB_DIR)/i18n/en-US.js
	$(NODE) --check $(WEB_DIR)/i18n/zh-CN.js
	$(NODE) --check $(WEB_DIR)/session-lock.js
	$(NODE) --check $(WEB_DIR)/app.js

node-test:
	$(NODE) --test $(WEB_DIR)/app.test.js $(WEB_DIR)/session-lock.test.js

node-test-i18n:
	$(NODE) --test $(WEB_DIR)/i18n/i18n.test.js

i18n-check: node-test-i18n
	@echo "i18n: registry tests passed"

browser-test:
	$(NODE) --test $(WEB_DIR)/app.browser.test.js

check: fmt-check test build test-duckdb node-check node-test node-test-i18n i18n-check browser-test

clean:
	rm -f $(BINARY)
