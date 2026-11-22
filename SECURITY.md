# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately, through
[GitHub security advisories](https://github.com/dsj7419/loadline/security/advisories/new), and not
in a public issue. Include the Loadline version (`loadline version`), your operating system, and the
steps that show the problem. You will get an acknowledgement, and updates until it is fixed.

Fixes go into the latest release only.

## Threat model

Loadline runs on one computer, for the user who installed it. It holds metadata about that user's
Claude Code sessions (account emails and plans, session names, project paths, models, token counts,
costs and states), the ingest token, and the edits it made to Claude Code's settings. It never holds
prompts, responses, tool input or output, file contents or credentials, so none of that can leak
through it.

What it defends against:

- **Web pages in your browser.** Any page can send requests to `127.0.0.1`. The server refuses a
  request whose `Host` is not `127.0.0.1:<port>` or `localhost:<port>`, and a state-changing
  request whose `Origin` is another site. Reading the dashboard's data needs the token or the
  `loadline_ui` cookie, which is `HttpOnly` and `SameSite=Strict`, and sending events needs the
  token, which no page has. The server sends no CORS headers, so a page cannot read its answers,
  and the dashboard runs under a content security policy that allows only its own files.
- **DNS rebinding.** A page that points its own domain at `127.0.0.1` still sends its own name as the
  `Host`, which the server refuses.
- **Other users on the same computer.** The token file is readable only by you, and on macOS and
  Linux install takes group and other permissions off the settings files it edits, which hold the
  token too. On Windows these files rely on the permissions of your profile folders, which by
  default admit only you, administrators and the system.
- **Malformed input.** Ingest request bodies are size-capped and decoded strictly, and ingest is
  rate-limited. The hook handler drops every field outside a short allowlist before anything leaves
  its process.
- **Damage to your settings.** Install and uninstall refuse a settings file that is not valid JSON,
  back it up first, write it atomically, and write nothing if it changed after you saw the diff.

## Out of scope, and known limits

- **Processes running as you, or as an administrator.** They can read the token, the settings and
  the history, and can do anything Loadline can. Loadline does not try to defend against them.
- **A process that takes Loadline's port first.** A local process that binds `127.0.0.1:<port>`
  before Loadline starts, such as one run by another user on a shared computer, receives what the
  hooks, status lines and OpenTelemetry exporter send: the ingest token and session metadata, never
  content. Use Loadline on a single-user computer, or set a non-default `port` in the config file
  and run `loadline install` again.
- **The login code on the browser's command line.** To sign the dashboard in, Loadline starts the
  browser with a one-time code in its URL. The code is valid for 60 seconds and only once, but on
  Linux other local users can read a process's command line through `/proc/<pid>/cmdline` while
  the browser starts.
- **The dashboard cookie's scope.** Browsers do not separate cookies by port, so the `loadline_ui`
  cookie, set for `127.0.0.1`, goes to every port on `127.0.0.1` that the browser visits. It does
  not rotate: it is derived from the token and changes only with it. To change both, stop the
  server, delete the `token` file in the data directory and run `loadline install` again.
- **Opening once per login.** `openOnStart: once_per_login` opens the dashboard on the first server
  start after the computer boots, not after each sign-in.
- **The shell Claude Code uses on Windows.** Install quotes the status line for Git Bash when it
  finds Git for Windows (through `CLAUDE_CODE_GIT_BASH_PATH`, `git.exe` on `PATH`, or the standard
  install folders), and for PowerShell otherwise. On an unusual Git install, such as a Scoop or
  portable one, its answer can differ from Claude Code's, and the status-line command would then run
  under a shell it was not quoted for. Which PowerShell Claude Code uses without Git Bash is not
  documented; when Loadline runs your status line for PowerShell, it uses PowerShell 7 (`pwsh`)
  if it is on `PATH`, and Windows PowerShell otherwise. `loadline doctor` reports which form is in
  use.
