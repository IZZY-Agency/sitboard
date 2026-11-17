# sitboard

A live board of the Claude Code sessions running on your machine.
It answers two questions at a glance: **who does what**, and **who needs me**.

![The sitboard board in tiles mode](https://sitboard.com/board.png)
*Sample data.*

## Install

```sh
brew install izzy-agency/tap/sitboard
```

Or build it from source (Go 1.27 and Node 22):

```sh
git clone https://github.com/IZZY-Agency/sitboard.git
cd sitboard
make build        # builds the web page, then bin/sitboard with the page embedded
```

## Run

```sh
sitboard
```

It starts a small web server on your machine and opens the board in your browser. It finds the running sessions by itself: nobody creates tasks in it, and the agents need no setup. When it cannot tell something, it says "unknown" instead of guessing.

Flags: `--port`, `--no-open`, `--no-github`, `--roots` (only agents working under these folders), `--stale-after` (default `20m`), `--version`, `--licenses` (sitboard's licence and the third-party notices).

## Three views

- **Board** (the default): one column per state, in this order: Needs you, Working, With coordinator, In review, Done · 24h, Idle.
- **Lanes**: one row per project, with a "Needs you" strip on top.
- **Teams**: your queue beside one panel per coordinator with its workers, and one panel of solo agents.

Project tiles or a side rail (you choose which) pick the projects every view shows. A project that spans several repositories shows as one entry with its parts.

Each card shows the session's name or title, its project, status and model, how long it has been in that state, and the pull requests it opened with their checks and review. With agterm, one click jumps to the session's terminal, and cards get better titles; without it, clicking a card shows the session's folder and name to copy.

## Security and privacy

- **Local only.** sitboard listens on `127.0.0.1`, never on your network.
- **A private link.** It prints a link that carries a secret for this run; your browser swaps it for a cookie. Requests without it, and requests from other websites, are refused.
- **Nothing leaves your machine**, except read-only GitHub lookups for pull request state, made through your own `gh` command. sitboard never reads or stores a GitHub token. With `--no-github` there is no network traffic at all.
- **Names, titles, folder paths, states and pull request links, never session text.** The page gets session names and titles, home-relative folder paths, repository names, states, and pull request numbers with their check status. Prompt text, code and tool output are never shown and never served.
- **No telemetry**, no update check, no crash reports.

## Requirements

- [Claude Code](https://claude.com/claude-code): sitboard shows its sessions.
- Optional: [`gh`](https://cli.github.com), logged in, for pull request state.
- Optional: agterm, for the one-click jump to a session's terminal and better card titles.

Tested on macOS. Linux builds are provided but not tested by us yet.

## Not there yet

- Codex sessions: only Claude Code sessions are shown.
- Catch-up: what happened since you last looked.
- Leftovers: merged work whose worktree, branch or session still exists.

See [CHANGELOG.md](CHANGELOG.md).

## Development

```sh
make check PRIVATE_FLAGS=--no-denylist   # lint, tests, web checks, contract freshness, release-script tests, private-material guard
make contract                            # regenerate web/src/contract.gen.ts from internal/contract
```

`scripts/check-private.sh` keeps private material out of the repository: home paths, tokens, email addresses and hidden characters. Maintainers also run it with a private deny-list of names (see `scripts/make-denylist.sh`); `--no-denylist` runs the generic rules only. Releases are made with the scripts described in [scripts/README.md](scripts/README.md).

## Licence

MIT. See [LICENSE](LICENSE). The third-party components sitboard ships with, and their licences, are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md); `sitboard --licenses` prints them too.
