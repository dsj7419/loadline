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
  Windows Loadline creates it, and each folder it creates on the way to it, with an access list of
  its own that admits only you, SYSTEM and Administrators and takes nothing from the folder above;
  on macOS and Linux with mode 0700. Install, the server and the creation of the token make an
  existing data directory private the same way, and refuse a data directory that is itself a
  symbolic link or junction, naming where it leads, which is the folder to set `LOADLINE_HOME` to:
  making the link private would leave that folder as open as it is. Install and the server also
  put back what the data directory passes down on each of Loadline's own files and folders that
  has anything else: access given to it explicitly, denials included, an access list of its own
  but the private one, or other entries than the three the data directory passes down, each once,
  which could keep you out of it. They do so before they read the token or the binary, so that
  `loadline install` puts right a token or binary you are kept out of. Install, the server and the
  creation of the token refuse a symbolic link or junction among them, which writing through would
  follow elsewhere, a file with another name, a hard link, which making it private would change
  wherever that name is too, and one that belongs to anyone but you, SYSTEM, Administrators or
  TrustedInstaller, since its owner may always change who can use it. The temporary name the token
  was written under, left beside it in its folder when the process that made it was ended or a
  scanner held the file, is no other name: install and the server remove it as far as they can,
  and go on. The data directory, and each folder of Loadline's that keeps you out, is made private
  without passing that on. Each file is put back as soon as it is judged, each folder once
  everything it holds has been, even when something later is refused, and the data directory
  passes its list down only when nothing is, so that nothing reaches a file elsewhere through a
  hard link before it is refused. They leave alone the files another Loadline process is still
  writing there under a temporary name, when they are files. Since everything in the data
  directory is made private with it, `LOADLINE_HOME` must be a folder of Loadline's own: install,
  the server and the creation of the token refuse one that holds anything else, and change
  nothing in it. Only the files that Windows Explorer and the macOS Finder leave in a folder
  (`desktop.ini`, `Thumbs.db`, `.DS_Store`) may be there too, and they are made private and judged
  by their owner as Loadline's own files are.
  `loadline doctor` fails while the data directory or one of Loadline's files in it lets anyone else in. If
  it did before, they may have read the token, so change it as described below. On macOS and Linux
  install also takes group and other permissions off the settings files it edits, which hold the
  token too, and off their backups. On Windows those files rely on the permissions of the folder
  they are in, which in your profile by default admits only you, administrators and the system.
- **Other users and the folders above the data directory.** Whoever can delete or rename a folder
  above the data directory, or give themselves the right to, can put a folder of their own, and a
  binary of their own, at the path the hooks run. Install and the server judge each folder above
  the data directory that exists, up to the volume root, and refuse the data directory, naming the
  folder, when anyone but you, SYSTEM and Administrators could move one aside:
  - on Windows, a folder above it that grants them FILE_DELETE_CHILD, WRITE_DAC (change
    permissions) or WRITE_OWNER (take ownership), or one below the volume root that grants them
    DELETE, by an allow entry of any kind;
  - on macOS and Linux, a folder above it that group or others can write to without the sticky bit;
  - everywhere, a folder above it, or the data directory itself, that belongs to anyone else: on
    Windows anyone but you, SYSTEM, Administrators or TrustedInstaller, on macOS and Linux anyone
    but you or root.

  They judge the folders that really hold the data directory as well as those its path names: when
  a junction, a symbolic link or, on Windows, a `subst` drive is on the way, the folders that hold
  where it leads are judged too. A data directory whose path cannot be followed that way is
  refused, since those folders would go unjudged. `loadline doctor` reports the same, for a data
  directory that does not exist yet too, saying that install would refuse to make it. The default
  location passes, and so does a `LOADLINE_HOME` directly in a drive root, such as `D:\loadline`,
  that you created; one deeper in a folder every user can modify, such as `D:\tools\loadline`,
  does not, so keep it in your user profile.
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
- **A refused server.** A server that refuses its data directory writes no log there, since it
  writes nothing into a folder it refused. A server a hook starts therefore says why only through
  `loadline doctor`, which reports the same refusal, and `loadline serve` run by hand prints it on
  its standard error. The hooks go on running the binary that is there.
- **A linked settings file.** When `settings.json`, or the config folder that holds it, is a link,
  for example into a dotfiles repository (a symbolic link, or on Windows a junction), install writes
  the token into the file it ends up in, and committing that repository would publish it. `loadline
  doctor` warns about either link.
- **Claude Code's own writes.** Claude Code rewrites `settings.json` when you change a setting, with
  permissions of its own choosing, which can let group or others read the token again. On macOS and
  Linux `loadline doctor` reports a settings file whose mode lets them in.
- **Backups of the settings.** Each backup holds the token the file held. Install and uninstall
  keep the newest three backups they made of each file and remove the older ones; they touch no
  other file.
- **The shell Claude Code uses on Windows.** Install quotes the status line for Git Bash when it
  finds Git Bash where Claude Code looks for it, in this order: the file `CLAUDE_CODE_GIT_BASH_PATH`
  names if it is a bash or sh that exists, `C:\Program Files\Git` or `C:\Program Files (x86)\Git`
  (those paths exactly, wherever `%ProgramFiles%` points), or the `bin` folder beside the folder
  that holds the first `git.exe` on `PATH`. Otherwise it quotes it for PowerShell. A per-user Git
  in `%LOCALAPPDATA%\Programs\Git` therefore counts only through its `git.exe` on `PATH`, as it
  does for Claude Code. Claude Code resolves a `PATH` entry that is not absolute, such as `cmd` or
  `\cmd`, against each session's own working directory, which install cannot know, so install
  skips such entries, and `loadline doctor` warns when one of them leads to a `git` from the
  folder it runs in ahead of the first absolute folder that holds `git.exe`. Claude Code's own
  lookup can still differ in other ways install cannot know: it searches each session's working
  directory first, and through `PATHEXT` a `git.cmd` or `git.bat` earlier on `PATH` can win. In
  such a setup the status-line command can run under a shell it was not quoted for; setting
  `CLAUDE_CODE_GIT_BASH_PATH` settles the shell for every session. Which PowerShell Claude Code uses
  without Git Bash is not documented; when Loadline runs your status line for PowerShell, it uses
  PowerShell 7 (`pwsh`) if it is on `PATH`, and Windows PowerShell otherwise. `loadline doctor`
  warns when the status line uses the PowerShell form, but cannot tell which shell Claude Code will
  actually use.

## Known limitations

These limits of the data-directory checks each need a setup you chose, such as a `LOADLINE_HOME`
outside your profile, or an unusual profile. The default location (`%LOCALAPPDATA%\loadline` on
Windows) on a standard profile meets none of them. Each one either fails closed, refusing to run,
or is narrow.

- **A file left over from before a refused run.**
  - *What it takes:* a `LOADLINE_HOME` that was not private yet, holding two things. One is a
    temporary file that another Loadline process began there before, whose name starts with
    `.token-`, `.put-` or `.loadline-`. The other is something install or the server refuses,
    such as a hard link, a link or a file that belongs to someone else, inside a folder of
    Loadline's that kept you out.
  - *Impact:* the refused run makes the data directory private and puts right what it has judged,
    but it passes the directory's list down only when nothing is refused. That temporary file keeps
    what it inherited before, which can let others read it. Later runs find the directory private
    and leave such a file to its writer.
  - *Mitigation:* keep `LOADLINE_HOME` inside your profile, or use the default. Otherwise delete
    the file and run `loadline install`.
- **A data directory that keeps you out.**
  - *What it takes:* a `LOADLINE_HOME` folder whose own access list does not let you list it, such
    as one copied or restored with another account's permissions.
  - *Impact:* install, the server and `loadline doctor` fail instead of putting it right. The
    message depends on what the list leaves you:
    - when it gives you nothing at all, the folder cannot even be opened to find the folders that
      hold it: "find the folders that hold <folder>: open <folder>: Access is denied.; set
      LOADLINE_HOME to a folder in your user profile";
    - when it lets you open the folder but not list it: "read the data directory: open <folder>:
      Access is denied."
  - *Mitigation:* use the default. Otherwise reset the folder's permissions (for example
    `icacls <folder> /reset`) and run `loadline install`.
- **A link further above the settings file.**
  - *What it takes:* a link, a symbolic link or on Windows a junction, two or more levels above
    `settings.json`, above the config folder that holds it, such as a linked home folder.
  - *Impact:* `loadline doctor` warns when `settings.json` or its config folder is a link, but not
    about links further up. Install then writes the token wherever that link leads without a
    warning, which matters when it leads into a repository that gets published.
  - *Mitigation:* use the default `~/.claude` in a standard profile, and keep the folders above a
    custom config folder unlinked.
- **Cloud and deduplicated files.**
  - *What it takes:* a data directory under a OneDrive folder with Files On-Demand, where
    Loadline's own files can be reparse points.
  - *Impact:* install and the server take a reparse point among Loadline's files for a link, and
    refuse the data directory as holding one. This fails closed, but the message names a link. A
    volume with data deduplication works: they take a deduplicated file for the ordinary file it is.
  - *Mitigation:* use the default, which is outside OneDrive, or keep `LOADLINE_HOME` inside your
    profile and out of such folders.
- **Volumes without a drive letter.**
  - *What it takes:* a `LOADLINE_HOME` on a volume that Windows gives no drive letter to, whether
    its path names that volume or a link on the way leads there. Examples are some RAM disks,
    container mounts and network redirectors.
  - *Impact:* Windows names the folders that really hold the data directory there only by the
    volume's GUID name (`\\?\Volume{…}\`), which Loadline judges no folder by. Install, the server
    and `loadline doctor` refuse it, and say why: "<folder> is on a volume Windows gives no drive
    letter to, as some RAM disks, container mounts and network redirectors are, so Loadline cannot
    check the folders that hold it: set LOADLINE_HOME to a folder on a disk with a drive letter,
    such as one in your user profile". This fails closed.
  - *Mitigation:* use the default, or set `LOADLINE_HOME` to a folder on a disk with a drive
    letter, such as one in your user profile.
- **Folders owned by an old account or a group.**
  - *What it takes:* the data directory, one of Loadline's files, or a folder above the data
    directory, that belongs to anyone but you, SYSTEM, Administrators or TrustedInstaller. One case
    is the SID of an account that no longer exists, after a profile was migrated or restored.
    Another is a domain group.
  - *Impact:* install and the server refuse the data directory, naming the folder and its owner,
    since that owner can always change who may use it. On such a profile the default location can
    be refused too.
  - *Mitigation:* use the default on a standard profile. Otherwise take ownership of the folder it
    names (for example `takeown /f <folder>`) and run `loadline install`.
- **A hard link under a temporary name.**
  - *What it takes:* a `LOADLINE_HOME` that was not private yet, into which someone put a hard
    link to a file elsewhere under one of the names Loadline writes files under before it puts
    them in place (`.token-…`, `.put-….tmp`, `.loadline-….tmp`).
  - *Impact:* install and the server leave such files to the Loadline process writing them, and
    do not open them, so they never refuse that link. When they put right the folder that holds
    it, or the data directory passes its list down, that list reaches the file elsewhere through
    the link, and changes who may use the file there.
  - *Mitigation:* keep `LOADLINE_HOME` inside your profile, or use the default, where nobody else
    can put anything.
