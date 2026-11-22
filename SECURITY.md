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
  request whose `Origin` is anything but the dashboard's own, `http://127.0.0.1:<port>` or
  `http://localhost:<port>`, port included. Reading the dashboard's data needs the token or the
  `loadline_ui` cookie, which is `HttpOnly` and `SameSite=Strict`, and sending events needs the
  token, which no page has. The server sends no CORS headers, so a page cannot read its answers,
  and the dashboard runs under a content security policy that allows only its own files.
- **DNS rebinding.** A page that points its own domain at `127.0.0.1` still sends its own name as the
  `Host`, which the server refuses.
- **Other users on the same computer.** The token file is readable only by you. The data directory
  that holds it, with the binary every hook runs, the spool and the history, admits only you: on
  Windows Loadline gives it an access list of its own that admits only you, SYSTEM and
  Administrators, and stops it inheriting from the folder above, so this holds for a
  `LOADLINE_HOME` outside your profile too, such as one on a second drive whose root lets every
  user in; on macOS and Linux it has mode 0700. Install, the server and the creation of the token
  each set it, and `loadline doctor` fails while it lets anyone else in. If it did before, they may
  have read the token, so change it as described below. On macOS and Linux install also takes group
  and other permissions off the settings files it edits, which hold the token too, and off their
  backups. On Windows those files rely on the permissions of the folder they are in, which in your
  profile by default admits only you, administrators and the system.
- **Another account's credentials.** A hook runs with its Claude Code session's environment, which
  holds that session's settings `env`: the ingest token in the OpenTelemetry headers, and any API key
  or token you keep there. The server a hook starts, the browser the server opens and the `claude`
  it runs for each account start without the variables of that session: every name that begins
  with `ANTHROPIC_`, `CLAUDE_`, `CLAUDECODE` or `OTEL_` is removed, so one account's key never signs
  in another account's `claude`. The one exception is `CLAUDE_CODE_GIT_BASH_PATH`, which holds no
  credential and without which Claude Code on Windows cannot find a Git Bash installed somewhere
  unusual. Each `claude` gets `CLAUDE_CONFIG_DIR` set for its own account. They keep the rest of
  your environment, cloud-provider credentials such as `AWS_*`, `GOOGLE_*` and `AZURE_*` included,
  which a Bedrock or Vertex account's own `claude` may need and which do nothing for an account
  that does not choose that provider.
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
  server (it exits `idleExitMinutes` after the last session ends, or end the `loadline serve`
  process), delete the `token` file in the data directory and run `loadline install` again.
- **Opening once per login.** `openOnStart: once_per_login` opens the dashboard on the first server
  start after the computer boots, not after each sign-in.
- **A symlinked settings file.** When `settings.json` is a symlink, for example into a dotfiles
  repository, install writes the token into the file it points to, and committing that repository
  would publish it. `loadline doctor` warns about such a link.
- **Claude Code's own writes.** Claude Code rewrites `settings.json` when you change a setting, with
  permissions of its own choosing, which can let group or others read the token again. On macOS and
  Linux `loadline doctor` reports a settings file whose mode lets them in.
- **Backups of the settings.** Each backup holds the token the file held. Install and uninstall
  keep the newest three backups they made of each file and remove the older ones; they touch no
  other file.
- **The shell Claude Code uses on Windows.** Install quotes the status line for Git Bash when it
  finds Git for Windows (through `CLAUDE_CODE_GIT_BASH_PATH`, `git.exe` on `PATH`, or the standard
  install folders), and for PowerShell otherwise. On an unusual Git install, such as a Scoop or
  portable one, its answer can differ from Claude Code's, and the status-line command would then run
  under a shell it was not quoted for. Which PowerShell Claude Code uses without Git Bash is not
  documented; when Loadline runs your status line for PowerShell, it uses PowerShell 7 (`pwsh`)
  if it is on `PATH`, and Windows PowerShell otherwise. `loadline doctor` warns when the status
  line uses the PowerShell form, but cannot tell which shell Claude Code will actually use.
