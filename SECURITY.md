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
  and go on. A data directory whose own permissions keep you from listing it, or on macOS and
  Linux from passing through it, is first let back into, where that widens nobody else's access:
  on Windows, install, the server and the creation of the token add one entry to its list, for you
  alone and for that folder alone, that lets you list it, after every entry it was given
  explicitly, so after each denial given to it, and before what it inherits, so that, as any entry
  given explicitly does, it outweighs for you a denial it inherits; on macOS and Linux they give
  you every right on it again. It keeps that until it is made private, also when something in it
  is refused then. Two of them doing so at once may leave that entry there twice, which lets in
  nobody else either. When you still cannot list it, as when a denial given to it keeps you out,
  which taking off would let in whoever else it keeps out, or when you may not change its
  permissions, they put them back exactly as they were and refuse it, naming its owner and what an
  administrator can do; `loadline doctor` says beforehand, changing nothing, which it will be.
  When two of them both find you still kept out, the one that puts back what it read after the
  other's change can leave that entry there, or on macOS and Linux your rights on it: that too
  lets in you alone, after every denial given to it, and you still cannot list it. One you may
  list but whose permissions you may not change cannot be made private, and is refused saying so.
  The data directory, and each folder of Loadline's that keeps you out, is made private without
  passing that on. Each file is put back as soon as it is judged, each folder once
  everything it holds has been, even when something later is refused, and the data directory
  passes its list down only when nothing is, so that nothing reaches a file elsewhere through a
  hard link before it is refused. The files another Loadline process is still writing there under
  a temporary name (`.token-…`, `.put-….tmp`, `.loadline-….tmp`) they judge where they are. They
  count each one's names first, and refuse one with a name outside its own folder as they refuse
  any other hard link: the token has its temporary name beside its own while it is linked into
  place, but no file Loadline writes has a name elsewhere. One with no other name they refuse when
  it belongs to anyone else, as they refuse Loadline's own files, and otherwise put back on it
  what the data directory passes down if it has anything else, as one begun there before the data
  directory was private has. They do so where it is, in every run, a refused one too, so that it
  never waits for a list passed down; the process writing it goes on through the handle it holds.
  One begun since has the private list, and they change nothing of it. A file that goes
  meanwhile, or whose deletion is pending, they pass over at every step, `loadline doctor`
  included, never failing on it. Windows refuses to let them read or change the permissions of a
  file whose deletion is pending with the same "access denied" as one whose permissions keep you
  out, as when it belongs to someone else or an OWNER RIGHTS entry takes your rights as its owner
  away. So when such a refusal lasts, they look at the file again, asking only to read its
  attributes, which the right to list its folder allows whatever the file's own permissions say,
  and which only a pending deletion refuses. A file they find there, and one whose permissions
  they would put right but you may not change, install, the server and `loadline doctor` refuse
  in the same words: "<file> does not let you both read and change who may use it, so Loadline
  cannot make sure it is private; it belongs to <owner>: remove it, or have an administrator
  remove it if you cannot, then run loadline install", or "its owner cannot be read" when they
  may not read that either. Its folder is yours, so you may remove it, unless it is read-only and
  its permissions also take away your right to change its attributes, which only an
  administrator may then do. A file with a second temporary name beside it in its folder, which
  no Loadline process makes, they neither judge by its owner nor put right, and doctor does not
  report it: it stays as it is, but for what the data directory passes down when it is first
  made private, which leaves what was given to the file explicitly, and a data directory that is
  private already passes nothing down again. The impact is low: no Loadline process writes into
  such a file, since each writes to a new file that it creates under a name of its own, and
  never opens one that is there already. A name they cannot judge, because its file goes or its
  deletion is pending, keeps the data directory, and the folder that holds it, from passing a
  list down, which would reach that file; the next run judges what is there then.
  Since everything in the data directory is made private with it, `LOADLINE_HOME` must be a
  folder of Loadline's own: install, the server and the creation of the token refuse one that
  holds anything else, and change nothing in it. Only the files that Windows Explorer and the
  macOS Finder leave in a folder (`desktop.ini`, `Thumbs.db`, `.DS_Store`) may be there too, and
  they are made private and judged by their owner as Loadline's own files are.
  The dashboard's browser profile, `browser` in the data directory, which a Chromium app window
  opens the dashboard in unless `browserProfile` is `default`, holds the `loadline_ui` cookie
  and whatever else the browser stores. Loadline creates it private before the browser first
  launches: on Windows with an access list of its own, which the data directory's list, passed
  down, does not reach, and on macOS and Linux with mode 0700. What the browser writes there is
  its own, and Loadline changes nothing in the profile and reads no file's content there: the
  browser gives files there access lists of its own on Windows, for its network sandbox, and keeps
  symbolic links there on macOS and Linux, for its process lock. Only on Windows, when Loadline
  offers to take back a data directory that an account this computer no longer has owns, does it
  list what the profile holds and look at each entry's attributes, to tell whether `takeown /r`
  may be offered (see "Folders owned by an old account or a group" below). Install, the server
  and `loadline doctor` judge the folder itself as they judge Loadline's own, and refuse a
  `browser` folder that is not private as Loadline makes it, since the files in it may have
  inherited other users' access: remove it, and Loadline makes it again.
  `loadline doctor` fails while the data directory or one of Loadline's files in it, one still
  being written among them, lets anyone else in. If it did before, they may have read the token,
  so change it as described below. On macOS and Linux install also takes group and other
  permissions off the settings files it edits, which hold the token too, and off their backups.
  On Windows those files rely on the permissions of the folder they are in, which in your profile
  by default admits only you, administrators and the system.
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
  cookie, set for `127.0.0.1`, goes to every port on `127.0.0.1` that the browser visits in the
  profile that holds it. In the dashboard's own profile, which a Chromium app window opens in by
  default, that is only Loadline's own pages, unless you browse elsewhere in that window. In your
  normal profile, with `browser: auto`, Firefox, a tab or `browserProfile: default`, it is every
  local page you open there. A cookie set in your normal profile earlier, by 0.1.0 or by an open
  that fell back to it, stays valid there until it expires 30 days after it was set, or until the
  token changes. It does not rotate: it is derived from the token and changes only with it. To
  change both, stop the server (`loadline stop`, or let it exit `idleExitMinutes` after the last
  session ends), delete the `token` file in the data directory and run `loadline install` again.
- **Opening once per login.** `openOnStart: once_per_login` opens the dashboard on the first server
  start after the computer boots, not after each sign-in.
- **A refused server.** A server that refuses its data directory writes no log there, since it
  writes nothing into a folder it refused. A server a hook starts therefore says why only through
  `loadline doctor`, which reports the same refusal, and `loadline serve` run by hand prints it on
  its standard error. The hooks go on running the binary that is there.
- **A linked settings file.** When `settings.json`, the config folder that holds it, or a folder
  above that such as a linked home folder, is a link, for example into a dotfiles repository (a
  symbolic link, or on Windows a junction), install writes the token into the file it ends up in,
  and committing that repository would publish it. `loadline doctor` warns about the link nearest
  the file. It leaves out the folders straight under the root, such as `/var` on macOS, whose links
  are the system's own layout.
- **Claude Code's own writes.** Claude Code rewrites `settings.json` when you change a setting, with
  permissions of its own choosing, which can let group or others read the token again. On macOS and
  Linux `loadline doctor` reports a settings file whose mode lets them in.
- **Stopping the server.** Whoever can read the token can also stop the server, with `loadline
  stop` or the request it sends, which takes the token and never the dashboard cookie. On macOS and
  Linux the token is in `settings.json`, which Claude Code may leave readable by others, as the
  item above says. The cost is availability alone: while no server runs, the hooks spool their
  events, and a session's next hook or status line starts the server again.
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

- **A data directory that keeps you out.**
  - *What it takes:* a `LOADLINE_HOME` folder whose own permissions keep you out in a way Loadline
    cannot undo without letting anyone else in: they deny you the right to list it, or they do not
    let you change them, as on a folder that Administrators own and Loadline now runs without
    elevation for, one that belongs to another account, or one whose OWNER RIGHTS entry withholds
    that from its owner. Permissions that only leave out your right to list it, on a folder you may
    change, no longer count: install and the server let you back in.
  - *Impact:* install, the server and `loadline doctor` refuse it, and leave its permissions exactly
    as they were, but for the entry, or your rights, that two runs at once may leave, as described
    above, which let in you alone.
    - One that keeps you from listing it: "<folder> keeps you out, and Loadline cannot let you
      back in without widening who else may use it; it belongs to <owner>: an administrator can
      reset it with icacls "<folder>" /reset, then run loadline install".
    - One you may list but not change, as with that OWNER RIGHTS entry: "<folder> cannot be made
      private, since you may not change who may use it; it belongs to <owner>: set LOADLINE_HOME
      to a folder of your own, or have an administrator reset it with icacls "<folder>" /reset,
      then run loadline install".
    - When its owner cannot be read either, as on a folder that gives you nothing, the sentence
      says "its owner cannot be read" in its place.
    - A path that holds `"`, `%`, `$`, `!`, a backtick, a typographic double quote, a control
      character or a line or paragraph separator, which a shell would read as another path, or a
      format character, such as a bidirectional override or a zero-width space, which shows another
      path than the command acts on, gets no command to paste: the sentence names the tool and the
      folder instead, by the same rule as the take-back below. On macOS and Linux a backslash does
      too, which fish reads as an escape even in single quotes.
    - On macOS and Linux the command gives the folder alone to you, with
      `sudo chown <you> '<folder>'`. Install then judges the owner of each of Loadline's files in it.
  - *Mitigation:* use the default. Otherwise have an administrator do what the message says, then
    run `loadline install`. A folder that belongs to another account is refused then for its owner,
    as above: set `LOADLINE_HOME` to a folder of your own instead.
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
  - *Impact:* install, the server and `loadline doctor` refuse the data directory, naming the
    folder and its owner, since that owner can always change who may use it. On such a profile the
    default location can be refused too.
  - *The repair Loadline offers (Windows):* when the owner is an account this computer no longer
    has, and the folder is in your user profile, the message ends with the command that takes it
    back: "…; it belongs to an account this computer no longer has, as after a profile was moved
    or restored: if the folder is yours, run takeown /f "<folder>" and then loadline install".
    - The command names the folder where it really is, every link followed and every short 8.3
      name in full: the folder Loadline checks is in your profile, and the one `takeown` takes.
    - For the data directory, or one of Loadline's files in it, the dashboard's browser profile
      and a file Loadline is still writing among them, the command names the data directory and
      adds `/r`, which takes all it holds too. It does so only while `takeown /r` would reach
      nothing else. The data directory must hold nothing at its top but Loadline's own entries and
      the files the system leaves there. Nothing anywhere below it, the files the browser keeps in
      its profile and those Loadline is still writing included, may be a reparse point of any
      kind, such as a junction or a symbolic link, or a file with a name outside the data
      directory, a hard link: `takeown /r` would follow either out of it. Nor may any folder below
      it be one you may not list, or anything in it be one Loadline cannot look at, since either
      could hide one. To tell, Loadline lists the tree and looks at each entry's attributes,
      reading no file's content and changing nothing.
      Otherwise the command names what the message reports alone, the data directory or the file
      or folder in it, without `/r`, and calls a file a file: "if the file is yours, run takeown /f
      "<file>" and then loadline install". A folder above the data directory never gets `/r`.
    - A path that holds `"`, `%`, `$`, `!`, a backtick or a typographic double quote, which
      Command Prompt or PowerShell would not take as written, or a control or format character
      (such as a zero-width space or a right-to-left override) or a line or paragraph separator,
      which a terminal would not show as it is, gets the tool's name instead of a command to paste.
    - An account counts as gone only when Windows is sure: it reports the account deleted, or it
      names no account for a SID of this computer's own accounts. A SID of another domain that it
      cannot name, as when no domain controller answers, may be an account that still exists: the
      message names it as "an account this computer cannot name right now" and offers nothing.
      Nothing is offered for a group or any other account that exists, nor on macOS and Linux.
    - Nothing is offered for a folder outside your profile either. Loadline places each folder
      where it really is, every link followed and every short 8.3 name in full, so neither can make
      a folder outside the profile pass for one in it.
  - *Mitigation:* use the default on a standard profile. Otherwise, if the folder or file is yours,
    run the command the message offers, or take ownership of the folder or file it names yourself
    (for example `takeown /f <folder>`), and run `loadline install`.
