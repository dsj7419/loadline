# Changelog

All notable changes to Loadline are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The first release, to be tagged 0.1.0.

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
- Token and cost totals by model, for today and the last 7 days.
- Alerts for a session waiting on you, an account past its weekly threshold or on pace to reach a
  limit before it resets, a spent daily budget, and a nearly full context window, as a banner, a
  count in the page title and a favicon dot, with opt-in browser notifications.
- `loadline install` and `loadline uninstall`, which edit Claude Code's `settings.json` in place,
  show the diff first and keep a backup; uninstall restores what was there before.
- `loadline doctor`, which checks the setup, the server and each feed.
- `loadline open`, `loadline status`, `loadline serve` (with `--demo`), `loadline hook`,
  `loadline tap` and `loadline version`.
- History in a local SQLite database, kept for 30 days, so a restarted server picks up where it
  left off.
- Release builds for Windows, macOS and Linux on amd64 and arm64, with checksums and build
  provenance attestations.

[Unreleased]: https://github.com/dsj7419/loadline/commits/main
