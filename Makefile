SHELL := /bin/bash

GO ?= go
NODE ?= node
BINARY ?= ./thoughtflow
GO_PACKAGES := ./...
WEB_DIR := internal/modules/application/thoughtflow/service/web
GO_FILES := $(shell find cmd internal -name '*.go' -type f)
# DuckDB ships as a static archive (libduckdb_static.a) under
# vendor/github.com/duckdb/duckdb-go-bindings/lib/<goos>-<goarch>/ and is
# linked directly into the thoughtflow binary on every supported
# platform. The cgo directives reference `-lstdc++` (no version suffix),
# which the system ld cannot resolve against the bare libstdc++.so.6
# shipped on minimal images. CI installs `libstdc++-dev` so the bare
# `libstdc++.so` symlink is on disk; local builds without the dev
# package fall back to a project-local `build/libstdcxx/libstdc++.so`
# symlink that the Makefile stages on demand.
LIBSTDCPP_STAGE := build/libstdcxx
LIBSTDCPP_SYMLINK := $(LIBSTDCPP_STAGE)/libstdc++.so
LIBSTDCPP_SOURCES := \
	/usr/lib/x86_64-linux-gnu/libstdc++.so.6 \
	/usr/lib/aarch64-linux-gnu/libstdc++.so.6
# `libstdc++-N-dev` ships its bare-name symlink under
# /usr/lib/gcc/<triple>/N/libstdc++.so, not under the default library
# search path. Add it to CGO_LDFLAGS directly so the system linker
# finds `-lstdc++` without us having to copy the symlink around.
LIBSTDCPP_GCC_LIBDIRS := \
	/usr/lib/gcc/x86_64-linux-gnu/14 \
	/usr/lib/gcc/x86_64-linux-gnu/13 \
	/usr/lib/gcc/x86_64-linux-gnu/12 \
	/usr/lib/gcc/x86_64-linux-gnu/11 \
	/usr/lib/gcc/aarch64-linux-gnu/14 \
	/usr/lib/gcc/aarch64-linux-gnu/13
CGO_LDFLAGS ?=

# If the system already exposes a bare `libstdc++.so` on any directory
# the linker searches, skip staging the project-local symlink.
# Otherwise stage one. Either way, append -L for any per-version gcc
# libdirs that host the bare symlink so cgo's `-lstdc++` resolves
# without us having to copy the symlink around.
LIBSTDCPP_FALLBACK_LDFLAGS = $(shell \
	staged=0; \
	for d in /usr/lib/x86_64-linux-gnu /usr/lib/aarch64-linux-gnu $(LIBSTDCPP_GCC_LIBDIRS); do \
		if [ -e $$d/libstdc++.so ]; then staged=1; break; fi; \
	done; \
	if [ $$staged -eq 0 ]; then \
		mkdir -p $(LIBSTDCPP_STAGE); \
		for s in $(LIBSTDCPP_SOURCES); do \
			if [ -e $$s ]; then \
				ln -sf $$s $(LIBSTDCPP_SYMLINK); \
				echo "-L$(CURDIR)/$(LIBSTDCPP_STAGE)"; \
				break; \
			fi; \
		done; \
	fi; \
	for d in $(LIBSTDCPP_GCC_LIBDIRS); do \
		[ -e $$d/libstdc++.so ] && echo "-L$$d"; \
	done)

.PHONY: help fmt fmt-check test build node-check node-test node-test-i18n i18n-check browser-test check clean

help:
	@printf '%s\n' \
		'Targets:' \
		'  fmt              Format Go files under cmd/ and internal/' \
		'  fmt-check        Verify Go formatting without changing files' \
		'  test             Run Go tests (DuckDB is the only backing store)' \
		'  build            Build the thoughtflow binary (statically links DuckDB)' \
		'  node-check       Run JavaScript syntax checks' \
		'  node-test        Run Node component tests' \
		'  node-test-i18n   Run i18n registry tests' \
		'  i18n-check       Verify every i18n key used in app.js/index.html is translated' \
		'  browser-test     Run embedded UI browser smoke tests' \
		'  e2e-test         Run end-to-end tests against a spawned thoughtflow binary' \
		'  check            Run the full validation matrix' \
		'  clean            Remove local build artifacts'

fmt:
	$(GO)fmt -w $(GO_FILES)

fmt-check:
	@test -z "$$($(GO)fmt -l $(GO_FILES))"

test:
	CGO_LDFLAGS="$(CGO_LDFLAGS) $(LIBSTDCPP_FALLBACK_LDFLAGS)" $(GO) test $(GO_PACKAGES)

build:
	CGO_LDFLAGS="$(CGO_LDFLAGS) $(LIBSTDCPP_FALLBACK_LDFLAGS)" $(GO) build -o $(BINARY) ./cmd/thoughtflow

node-check:
	$(NODE) --check $(WEB_DIR)/vendor/markdown-it.min.js
	$(NODE) --check $(WEB_DIR)/i18n/index.js
	$(NODE) --check $(WEB_DIR)/i18n/en-US.js
	$(NODE) --check $(WEB_DIR)/i18n/zh-CN.js
	$(NODE) --check $(WEB_DIR)/session-lock.js
	$(NODE) --check $(WEB_DIR)/app.js
	$(NODE) --check $(WEB_DIR)/api.e2e.test.js
	$(NODE) --check $(WEB_DIR)/events.e2e.test.js

node-test:
	$(NODE) --test $(WEB_DIR)/app.test.js $(WEB_DIR)/session-lock.test.js

node-test-i18n:
	$(NODE) --test $(WEB_DIR)/i18n/i18n.test.js

i18n-check: node-test-i18n
	@echo "i18n: registry tests passed"

browser-test:
	$(NODE) --test $(WEB_DIR)/app.browser.test.js

e2e-test:
	$(NODE) --test $(WEB_DIR)/api.e2e.test.js $(WEB_DIR)/events.e2e.test.js

check: fmt-check test build node-check node-test node-test-i18n i18n-check browser-test e2e-test

clean:
	rm -f $(BINARY)
	rm -rf build/
