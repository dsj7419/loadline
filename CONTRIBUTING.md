# Contributing

Thanks for your interest in Loadline. Bug reports, fixes and improvements are welcome. For a larger
change, please open an issue first, so we can agree on the approach before you put work into it.
Report security problems privately, as [SECURITY.md](SECURITY.md) describes.

## What you need

- Go 1.26. `go.mod` pins the exact toolchain, which the `go` command fetches for you.
- Node.js 24 and npm, for the dashboard under `web/`.
- GNU make. On Windows, run it from Git Bash.
- A C compiler, for the race detector the tests run under: GCC or Clang, or on Windows a MinGW-w64
  GCC. Without one, run `make test RACE=` to test without the race detector; CI always uses it.
- [golangci-lint](https://golangci-lint.run) v2 and
  [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) on your `PATH`.

## Checks

`make check` rebuilds the dashboard and runs the Go checks, in this order:

| Target | What it does |
| --- | --- |
| `make web` | Installs the dashboard's dependencies with `npm ci` and builds it into `internal/web/dist`. |
| `make test` | Runs the Go tests under the race detector. |
| `make lint` | Runs `golangci-lint`. |
| `make vuln` | Runs `govulncheck`. |
| `make build` | Builds the static release binary into `bin/`. |

The dashboard has checks of its own, which CI runs too:

```sh
cd web
npm run typecheck
npm test
npm run e2e   # Playwright; first run `npx playwright install chromium`
```

`npm run e2e` starts `loadline serve --demo` on port 18289 in a temporary `LOADLINE_HOME`. It
compiles the server from source, or uses the binary that `LOADLINE_BIN` names.

CI also lints the Windows and macOS builds, which you can do from any system:
`GOOS=windows CGO_ENABLED=0 golangci-lint run`.

## The committed dashboard bundle

The Go binary embeds the dashboard from `internal/web/dist`, which is committed, so that
`go install` works without Node.js. After changing anything under `web/` (sources, `index.html`,
`build.mjs` or dependencies), run `make web` and commit the updated `internal/web/dist` with the
change. The build is deterministic, and CI fails when the committed bundle differs from a fresh
build.

## Tests

- Write the failing test first, and test behaviour rather than implementation.
- Tests never touch your real Claude Code or Loadline files: they work under `t.TempDir()`, with
  `LOADLINE_HOME` pointing there, and listen on ports the system assigns.
- One Windows test binds a wildcard address, which raises a firewall prompt on a developer machine,
  so it runs only when `CI=true`, as it is on GitHub Actions.

## Dependencies

Keep them few. When a change adds or removes a Go module linked into the binary, or a package
bundled into the dashboard, update [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) to match.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org): `feat(install): …`,
  `fix(server): …`, `test(tracker): …`.
- Keep each commit to one change, with its tests, and leave the tree passing `make check`.
- Record user-visible changes under `Unreleased` in [CHANGELOG.md](CHANGELOG.md).
