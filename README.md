<p align="center">
  <a href="https://sitboard.com"><img src=".github/sitboard-banner.svg" alt="sitboard: every Claude Code session on your machine, on one board" width="880"></a>
</p>

<p align="center">
  <a href="https://github.com/IZZY-Agency/sitboard/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/IZZY-Agency/sitboard/ci.yml?branch=main&amp;label=CI" alt="CI"></a>
  <a href="https://github.com/IZZY-Agency/sitboard/releases/latest"><img src="https://img.shields.io/github/v/release/IZZY-Agency/sitboard?label=release&amp;color=f5c542" alt="Latest release"></a>
  <a href="https://github.com/IZZY-Agency/homebrew-tap"><img src="https://img.shields.io/badge/brew-izzy--agency%2Ftap%2Fsitboard-f5c542" alt="Homebrew formula"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/IZZY-Agency/sitboard?color=f5c542" alt="MIT licence"></a>
</p>

# sitboard

A live board of the Claude Code sessions running on your machine.

sitboard is a small command-line tool: it starts a web server on your machine
and opens the board in your browser. It answers two questions at a glance:
**who does what**, and **who needs me**. It finds the running sessions by
itself: nobody creates tasks in it, and the agents need no setup. When it
cannot tell something, it says "unknown" instead of guessing.

[Website](https://sitboard.com) ·
[Releases](https://github.com/IZZY-Agency/sitboard/releases) ·
[Issues](https://github.com/IZZY-Agency/sitboard/issues)

![The sitboard board in tiles mode](https://sitboard.com/board.png)
*Sample data.*

## What it does

- **A card per session.** Each card shows the session's name or title, its
  project, status and model (a Claude model in its family's colour: Fable,
  Opus, Sonnet, Haiku), how long it has been in that state, the pull requests
  it opened with their checks and review, and, on the Board, links to the
  claude.ai artifacts it published.
- **Board** (the default view). One column per state, in this order: Needs
  you, Working, With coordinator, In review, Done · 24h, Idle.
- **Lanes.** One row per project, with a "Needs you" strip on top.
- **Teams.** Your queue beside one panel per coordinator with its workers, and
  one panel of solo agents.
- **Project tiles or a side rail.** You choose which; they pick the projects
  every view shows. A project that spans several repositories shows as one
  entry with its parts. A grouping you do not want can be forgotten from the
  page; that folder stays apart until no agent has sat in it for two minutes.
- **A jump to the terminal.** With [agterm](https://github.com/umputun/agterm),
  one click jumps to the session's terminal, and cards get better titles;
  without it, clicking a card shows the session's folder and name to copy.

## Privacy

- **Local only.** sitboard listens on `127.0.0.1`, never on your network.
- **A private link.** It prints a link that carries a secret for this run; your browser swaps it for a cookie. Requests without it, and requests from other websites, are refused.
- **Nothing leaves your machine**, except read-only GitHub lookups for pull request state, made through your own `gh` command. sitboard never reads or stores a GitHub token. With `--no-github` there is no network traffic at all.
- **No prompts, code or tool output.** The page gets session names and titles, home-relative folder paths, repository names with their owner ("owner/name"), agterm workspace names, pull request numbers and titles with their state, checks, review and draft flag, the numbers of the issues they close, and the links and titles of the artifacts a session published, plus derived fields such as states, times, model ids, subagent counts, the versions of the tools it reads, and sitboard's own version, commit, operating system and architecture. A session's name is the one Claude Code records for it or, when it has none, its agterm terminal's name. A session's title is its open pull request's title or, with agterm, the short title Claude Code puts on its terminal, which is Claude Code's own summary of the task; without either, it is the session's name, its terminal's name or its folder's name. Prompt text, code and tool output are never shown and never served. The only free text the page gets from a transcript is an artifact's title (the title its own publish reported), cleaned and shortened.
- **No telemetry**, no update check, no crash reports.

## Install

```sh
brew install izzy-agency/tap/sitboard
```

The formula lives in [IZZY-Agency/homebrew-tap](https://github.com/IZZY-Agency/homebrew-tap)
and installs the archive of the
[latest release](https://github.com/IZZY-Agency/sitboard/releases/latest) for
your machine; `brew upgrade` keeps it current. You can also download that
archive yourself and put `sitboard` on your `PATH`.

The macOS builds, for Apple silicon and Intel, are signed with a Developer ID
and notarized. Tested on macOS. Linux builds are provided but not tested by us
yet.

## Run

```sh
sitboard
```

It prints its private link for this run and opens the board in your browser.

Flags: `--port`, `--no-open`, `--no-github`, `--roots` (only agents working under these folders), `--stale-after` (default `20m`), `--version`, `--licenses` (sitboard's licence and the third-party notices).

## Requirements

- [Claude Code](https://claude.com/claude-code): sitboard shows its sessions.
- Optional: [`gh`](https://cli.github.com), logged in, for pull request state.
- Optional: [agterm](https://github.com/umputun/agterm), for the one-click jump to a session's terminal and better card titles.

## Not there yet

- Codex sessions: only Claude Code sessions are shown.
- Catch-up: what happened since you last looked.
- Leftovers: merged work whose worktree, branch or session still exists.

See [CHANGELOG.md](CHANGELOG.md).

## Build from source

You need Go 1.27 and Node 22.

```sh
git clone https://github.com/IZZY-Agency/sitboard.git
cd sitboard
make build        # builds the web page, then bin/sitboard with the page embedded
```

For development:

```sh
make check PRIVATE_FLAGS=--no-denylist   # lint, tests, web checks, contract freshness, release-script tests, private-material guard
make contract                            # regenerate web/src/contract.gen.ts from internal/contract
```

`scripts/check-private.sh` keeps private material out of the repository: home paths, tokens, email addresses and hidden characters. Maintainers also run it with a private deny-list of names (see `scripts/make-denylist.sh`); `--no-denylist` runs the generic rules only.

Releases are made by the maintainers, with the scripts described in [scripts/README.md](scripts/README.md).

## How this repository is maintained

sitboard is developed in a private repository. Each release is published here
as a single commit tagged with its version, so a release tag always points at
the tree its build came from. Small updates between releases (this README, CI,
tooling) arrive as commits without a tag. Bug reports and feature requests go
in [Issues](https://github.com/IZZY-Agency/sitboard/issues).

## Licence

[MIT](LICENSE). The third-party components sitboard ships with, and their licences, are listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md); `sitboard --licenses` prints them too.
