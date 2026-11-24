# Changelog

All notable changes to Loadline are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `loadline stop [--port N]`, which asks the running server to stop and waits until its process
  has exited. It uses a new route, `POST /api/stop`, which takes the ingest token and never the
  dashboard cookie.
- `loadline install` hands a running server over to the binary it puts in place: it asks the
  server to stop, waits until it has, and starts the new one, so an upgrade takes effect without
  stopping the server by hand. After a change of `port`, it moves the running server from the old
  port to the new one, and leaves running one that you started with `serve --port`, saying how to
  stop it. A server that starts while another is still stopping waits for it, through a lock on
  `server.lock` in the data directory. One that cannot start because another server holds the data
  directory names that server and the `loadline stop --port` that stops it. (#25)
- The `browserProfile` config key: `dedicated`, the default, or `default` to open the dashboard's
  app window in your normal browser profile, as 0.1.0 did. (#6)

### Changed

- A Chromium-family browser opens the dashboard's app window in a browser profile of its own,
  `browser` in the data directory, so no extension from your own profile runs beside it and its
  sign-in cookie stays apart from your browsing. When that profile cannot be used, the dashboard
  still opens, in your normal profile, and Loadline says why. A browser given as a batch file
  (`.cmd` or `.bat`) opens it in your normal profile, as before, since Windows runs a batch file
  through `cmd.exe`, which would cut a profile path at an `&` or `^`. (#6)
- The dashboard is built with Preact 11 and TypeScript 7.

### Fixed

- An account's `label` in `config.json` is read without the spaces around it, as its `plan` is:
  a blank label falls back to the derived name instead of giving the card a blank title, and a
  label longer than 32 characters is refused.
- `loadline doctor` warns of a link in any folder above the config folder, not only the nearest,
  naming where it leads and so where install writes the ingest token. A settings file that a
  cloud service keeps in sync is no longer taken for a link. (#9)
- Building the dashboard's views no longer copies the history each time, so the server uses far
  less memory: a month's view of an account that spent stretches on usage credits allocates about
  8 MB instead of 135 MB. (#14)
- When two processes run one session, as after `--resume` while the first still runs, each
  process's rate-limit readings are judged on their own: a reset that keeps the window's reset
  time shows at once, and the other process's older, higher reading no longer comes back. (#15)
- When several long model calls overlap as an account crosses 100%, its time on usage credits is
  no longer split into several stretches: each response is dated from when its call began. (#16)
- On Windows, install looks for Git Bash where Claude Code looks for it: the file
  `CLAUDE_CODE_GIT_BASH_PATH` names, then `C:\Program Files\Git` and `C:\Program Files (x86)\Git`,
  then beside the first `git.exe` in a `PATH` folder given in full. It skips relative `PATH`
  entries such as `\cmd`, which Claude Code resolves against each session's working directory,
  and `loadline doctor` warns when one of them leads to a `git`. Typed in Git Bash or another MSYS
  shell, install and `loadline doctor` leave out the folders that shell puts in front of `PATH`
  for itself, and so choose as they would in PowerShell or cmd; an MSYS2 git with its bash beside
  it still counts. If you start Claude Code from Git Bash, set `CLAUDE_CODE_GIT_BASH_PATH` in your
  settings' `env`, and Claude Code and install use Git Bash whichever terminal starts them. (#20)
- Install recognises its own status line however its path is quoted, such as a hand-edited
  PowerShell `&'C:/…/loadline.exe' tap`, and no longer wraps it a second time. (#21)
- An upgrade on Windows no longer leaves the old binary behind: install and the server remove
  `bin\loadline.exe.old` once nothing runs it. (#22)
- On Windows, install and uninstall work when the config folder or the home folder is reached
  through a junction, as after moving it to another drive, instead of failing with "The system
  cannot find the path specified". `loadline doctor` follows a linked settings file below such a
  folder too, instead of calling it a link that cannot be followed. (#54)
- A healthy data directory could be refused as holding a hard link when its check ran slowly just
  as another Loadline process put the token in place.

### Security

- A temporary file that another Loadline process began before the data directory was private, as
  one left by a refused install or server can be, is made private where it is, in every run. One
  that belongs to someone else, or whose permissions you may not change, is refused, and
  `loadline doctor` reports one that lets anyone else in. (#7)
- A data directory whose own permissions keep you from listing it is let back into, for you
  alone, when that widens nobody else's access, instead of failing. Otherwise it is refused,
  naming its owner and what an administrator can do, and `loadline doctor` says beforehand which
  it will be. (#8)
- On Windows, a data directory in a folder that a cloud service such as OneDrive keeps in sync is
  refused, naming the folder and the service, since the token and the binary every hook runs would
  be copied to the cloud. Other reparse points are refused in words that say what they are, and a
  deduplicated file is accepted as the ordinary file it is. (#10)
- A data directory on a volume that Windows gives no drive letter to is still refused, now with a
  message that says why and what to do. (#11)
- On Windows, when the data directory, one of Loadline's files in it, or a folder above it belongs
  to an account this computer no longer has, and is in your profile, the refusal ends with the
  `takeown` command that takes it back. Loadline names the command and never runs it. (#12)
- A hard link planted under one of Loadline's temporary file names is refused, as a hard link
  under any of its other names is. (#17)
- On ReFS volumes, such as a Dev Drive, files are told apart by their full 128-bit file ID, so
  another file there can no longer be taken for the token's leftover temporary file. (#18)

### Notes

- Upgrading from 0.1.0 still needs the old server ended by hand once, since a 0.1.0 server cannot
  be asked to stop: install prints the command that ends it. Later upgrades hand the server over
  by themselves.
- Going back to 0.1.0 means deleting `browser` and `server.lock` from the data directory first.
  End your Claude Code sessions, whose status lines would start the server again, run
  `loadline stop`, close the dashboard's window, then delete both and install 0.1.0. Until then
  0.1.0 refuses the data directory with "\<data dir> holds server.lock, which is not Loadline's:
  LOADLINE_HOME must be a folder of Loadline's own, since Loadline makes it, and all it holds,
  private; set LOADLINE_HOME to a new folder", or the same sentence naming `browser`. It mentions
  `LOADLINE_HOME` even when you never set it and use the default data directory.
- On Linux and macOS this release is verified in CI only.
- For a patch release, this one adds a command (`loadline stop`), a route (`POST /api/stop`) and a
  config key (`browserProfile`). Each is kept small, and the one change to what you see by
  default, the dashboard's own profile, is undone with `"browserProfile": "default"`.
- Thanks to [@O1sumitkumar](https://github.com/O1sumitkumar) for the account label fix (#23).

## [0.1.0] - 2026-11-22

The first release. It has been run day to day on Windows with two subscription accounts. The macOS
and Linux builds pass the full test suite in CI but have not yet been used with live sessions;
reports from those systems are welcome.

### Added

- A live dashboard of every Claude Code session on the computer, grouped by state (needs you,
  working, idle, parked, ended), with each session's account, model and effort, context use,
  tokens left, cost, estimated share of its account's last hour, running subagents and last
  activity.
- A detail panel for each session: usage by model, a timeline of its states, cache hit ratio,
  lines changed, project path and session ID.
- An account card for each Claude Code config directory: the 5-hour and weekly limits Claude Code
  reports, when they reset, the burn rate, and whether the account is on pace to run out first.
  Accounts that pay per token show their spend, estimated at list price, against an optional daily
  budget.
- A pace for each limit window, such as `×1.4`: how fast the window is being used, as a multiple
  of the rate that would reach 100% just as it resets, coloured by how it compares with that rate.
- Usage credits for subscription accounts that keep working past a plan limit: an ON CREDITS badge
  while the account is on them, and a credits row with what it has spent since, estimated at list
  price, against the credits recorded in `config.json`. A spend limit that the status line reports
  replaces the estimate.
- Token and cost totals by model, for today and the last 7 days.
- Alerts for a session waiting on you, an account past its weekly threshold or on pace to reach a
  limit before it resets, an account that starts using usage credits or has spent 80% of them, a
  spent daily budget, and a nearly full context window, as a banner, a count in the page title and
  a favicon dot, with opt-in browser notifications.
- `loadline install` and `loadline uninstall`, which edit Claude Code's `settings.json` in place,
  show the diff first and keep a backup; uninstall restores what was there before.
- `loadline doctor`, which checks the setup, the server and each feed.
- `loadline open`, `loadline status`, `loadline serve` (with `--demo`), `loadline hook`,
  `loadline tap` and `loadline version`.
- History in a local SQLite database, kept for 30 days, so a restarted server picks up where it
  left off.
- Release builds for Windows, macOS and Linux on amd64 and arm64, with checksums and build
  provenance attestations.

[Unreleased]: https://github.com/dsj7419/loadline/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/dsj7419/loadline/releases/tag/v0.1.0
