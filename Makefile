# Developer entry points. Needs GNU make; on Windows run it from Git Bash.

# The race detector needs cgo, and therefore a C compiler. On a machine
# without one, run `make test RACE=` to test without it; CI always keeps it.
RACE ?= -race

# Release builds pass VERSION explicitly; otherwise it comes from git, and
# falls back to "dev" when there is no tag or commit to describe.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/dsj7419/loadline/internal/cli.Version=$(VERSION)

EXE := $(shell go env GOEXE)

.DEFAULT_GOAL := build
.PHONY: test lint vuln web build check

test:
	go test $(RACE) ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

# The UI bundle is committed under internal/web/dist, so this only needs to run
# after the sources under web/ change.
web:
	cd web && npm ci && npm run build

# The shipped binary is static, so cgo stays off here but not for `test`.
build: export CGO_ENABLED = 0
build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/loadline$(EXE) ./cmd/loadline

# check runs its steps one at a time, in order, even under -j: test and build
# embed the bundle, so they must not start before web has rebuilt it.
.NOTPARALLEL: check
check: web test lint vuln build
