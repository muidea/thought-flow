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
LIBSTDCPP_HOST_GCC_LIBDIRS := \
	/usr/lib/gcc/x86_64-linux-gnu/14 \
	/usr/lib/gcc/x86_64-linux-gnu/13 \
	/usr/lib/gcc/x86_64-linux-gnu/12 \
	/usr/lib/gcc/x86_64-linux-gnu/11
# Cross gcc places its libstdc++ under /usr/lib/gcc-cross/<triple>/N/
# (Debian/Ubuntu cross convention) or under the target sysroot gcc dir.
LIBSTDCPP_CROSS_GCC_LIBDIRS := \
	/usr/lib/gcc-cross/aarch64-linux-gnu/14 \
	/usr/lib/gcc-cross/aarch64-linux-gnu/13 \
	/usr/lib/gcc-cross/aarch64-linux-gnu/12 \
	/usr/lib/gcc/aarch64-linux-gnu/14 \
	/usr/lib/gcc/aarch64-linux-gnu/13
# `libstdc++-N-dev-<arch>-cross` also installs the bare libstdc++.so
# symlink under the target sysroot (e.g. /usr/aarch64-linux-gnu/lib).
# Probe it so cross-builds pick the right artifact.
LIBSTDCPP_CROSS_SYSROOT_LIBDIRS := \
	/usr/aarch64-linux-gnu/lib \
	/usr/x86_64-linux-gnu/lib
CGO_LDFLAGS ?=

# Pick the right set of libdirs to probe based on whether we are
# building natively or cross-compiling. Mixing host-arch libstdc++.so
# into the cross ld path makes it skip them as "incompatible" while
# still cluttering the search, so cross-builds only probe cross-target
# directories.
LIBSTDCPP_FALLBACK_LDFLAGS = $(shell \
	staged=0; \
	if [ -n "$(GOOS)$(GOARCH)" ]; then \
		probe_dirs="$(LIBSTDCPP_CROSS_GCC_LIBDIRS) $(LIBSTDCPP_CROSS_SYSROOT_LIBDIRS)"; \
	else \
		probe_dirs="/usr/lib/x86_64-linux-gnu /usr/lib/aarch64-linux-gnu $(LIBSTDCPP_HOST_GCC_LIBDIRS) $(LIBSTDCPP_CROSS_SYSROOT_LIBDIRS)"; \
	fi; \
	for d in $$probe_dirs; do \
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
	for d in $$probe_dirs; do \
		[ -e $$d/libstdc++.so ] && echo "-L$$d"; \
	done)

.PHONY: help fmt fmt-check test build node-check node-test node-test-i18n i18n-check check clean

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
		'  e2e-test         Run end-to-end tests against a spawned thoughtflow binary' \
		'  check            Run the full validation matrix' \
		'  clean            Remove local build artifacts'

fmt:
	$(GO)fmt -w $(GO_FILES)

fmt-check:
	@test -z "$$($(GO)fmt -l $(GO_FILES))"

test:
	CGO_LDFLAGS="$(CGO_LDFLAGS) $(LIBSTDCPP_FALLBACK_LDFLAGS)" $(GO) test $(GO_PACKAGES)

GO_LDFLAGS ?= -s -w
GO_GCFLAGS ?=

build:
	CGO_LDFLAGS="$(CGO_LDFLAGS) $(LIBSTDCPP_FALLBACK_LDFLAGS)" $(GO) build -trimpath -gcflags "$(GO_GCFLAGS)" -ldflags "$(GO_LDFLAGS)" -o $(BINARY) ./cmd/thoughtflow

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

e2e-test:
	$(NODE) --test $(WEB_DIR)/api.e2e.test.js $(WEB_DIR)/events.e2e.test.js

check: fmt-check test build node-check node-test node-test-i18n i18n-check e2e-test

clean:
	rm -f $(BINARY)
	rm -rf build/
