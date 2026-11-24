# Contributing

Thanks for your interest in Loadline. Bug reports, fixes and improvements are welcome. For a larger
change, please open an issue first, so we can agree on the approach before you put work into it.
Report security problems privately, as [SECURITY.md](SECURITY.md) describes. Everyone taking part
agrees to the [Code of Conduct](CODE_OF_CONDUCT.md).

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

### Data directories another account owns

Install, the server and doctor refuse a data directory that belongs to another account. The
`TestIntegration_` tests in `internal/cli` check this against a real second account, which a
developer machine seldom has set up, so each skips unless its variable is set, and fails when the
variable names something it cannot use. CI runs them on every platform. To run them yourself:

- **Windows:** from an elevated PowerShell. The tests give a temporary folder to the account you
  name, and to one this computer no longer has, which takes the SeRestorePrivilege an elevated
  shell holds, and give it back at their end.

  ```powershell
  $env:LOADLINE_IT_OTHER_ACCOUNT = '<name>'
  go test -race -count=1 -v -run TestIntegration_ ./internal/cli/
  ```

  Name a local account other than your own. To make one for the purpose, which nobody can sign in
  to, run `New-LocalUser -Name loadline-it -NoPassword -Disabled` in an elevated Windows
  PowerShell first, and `Remove-LocalUser -Name loadline-it` there once you are done. Windows
  PowerShell has these cmdlets built in; some builds of PowerShell 7 load them only after
  `Import-Module Microsoft.PowerShell.LocalAccounts -UseWindowsPowerShell`.

- **Linux and macOS:** name a folder that another account owns. It must hold a `config.json` that
  names a free port, so that doctor never asks a Loadline you may be running on the default one.

  ```sh
  sudo install -d -o nobody -m 0755 /tmp/loadline-it
  port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
  printf '{"port":%s}\n' "$port" | sudo tee /tmp/loadline-it/config.json > /dev/null
  sudo chown nobody /tmp/loadline-it/config.json
  sudo chmod 0644 /tmp/loadline-it/config.json
  LOADLINE_IT_FOREIGN_DIR=/tmp/loadline-it go test -race -count=1 -v -run TestIntegration_ ./internal/cli/
  sudo rm -r /tmp/loadline-it
  ```

## Dependencies

Keep them few. When a change adds or removes a Go module linked into the binary, or a package
bundled into the dashboard, update [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) to match.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org): `feat(install): …`,
  `fix(server): …`, `test(tracker): …`.
- Keep each commit to one change, with its tests, and leave the tree passing `make check`.
- Record user-visible changes under `Unreleased` in [CHANGELOG.md](CHANGELOG.md).
- A pull request is squash-merged once every CI check passes and the maintainer has reviewed it.
  On a first-time contributor's pull request, the checks start after the maintainer approves them.
