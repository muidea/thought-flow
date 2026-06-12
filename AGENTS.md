# Repository Guidelines

## Project Structure & Module Organization

ThoughtFlow is a Go 1.24 local-first service with a single binary entry point in `cmd/thoughtflow`. Domain modules live under `internal/modules`, with the HTTP/UI application in `internal/modules/application/thoughtflow` and feature modules such as `capture`, `expander`, `refiner`, `search`, `topic`, and `git_sync`. Shared libraries are in `internal/pkg`, including config, stores, markdown, event streaming, search, AI helpers, and workspace utilities.

Embedded web assets and browser-facing tests are in `internal/modules/application/thoughtflow/service/web`. Product and implementation docs are in `doc`, CI workflows are in `.github/workflows`, and vendored Go dependencies are kept in `vendor`.

## Build, Test, and Development Commands

- `make help`: list available targets.
- `make fmt`: run `gofmt` on Go files under `cmd/` and `internal/`.
- `make fmt-check`: verify Go formatting without modifying files.
- `make test`: run all Go tests with DuckDB linking flags prepared by the Makefile.
- `make build`: build `./thoughtflow` from `./cmd/thoughtflow`.
- `make node-check`: syntax-check embedded UI JavaScript.
- `make node-test`: run Node component tests.
- `make browser-test`: run embedded UI browser smoke tests.
- `make e2e-test`: run API and SSE end-to-end tests.
- `make check`: run the full local validation matrix used by CI.

Run locally with `make build` then `./thoughtflow`; the default UI is `http://127.0.0.1:8080/`.

## Coding Style & Naming Conventions

Use standard Go formatting and idioms; keep package names short, lowercase, and purpose-driven. Place new reusable code in `internal/pkg/<name>` and feature-specific business logic in the matching `internal/modules/<feature>/biz` package. Go tests should sit beside the code as `*_test.go`.

For web code, keep plain JavaScript, CSS, HTML, and i18n files in the existing `service/web` layout. When adding visible UI strings, update both `i18n/en-US.js` and `i18n/zh-CN.js` and keep keys centralized in `i18n/keys.js`.

## Testing Guidelines

Prefer focused unit tests for stores, parsers, services, and module behavior. Use Go table tests where they simplify cases. Run `make test` for backend changes, relevant Node targets for web changes, and `make check` before opening a PR. Browser or API behavior changes should include `*.browser.test.js` or `*.e2e.test.js` coverage when applicable.

## Commit & Pull Request Guidelines

Recent history uses Conventional Commit-style subjects such as `feat(capture): ...`, `fix(git_sync): ...`, `refactor(ai): ...`, and `docs: ...`. Keep subjects imperative and scoped when useful.

PRs should include a concise description, tests run, linked issues when relevant, and screenshots or short recordings for UI changes. Note configuration, migration, or workspace data impacts explicitly.
