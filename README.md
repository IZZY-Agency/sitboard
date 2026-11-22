<p align="center">
  <a href="https://sitboard.com"><img src=".github/sitboard-banner.svg" alt="sitboard: the Claude Code and Codex sessions on your machine, on one board" width="880"></a>
</p>

<p align="center">
  <a href="https://github.com/IZZY-Agency/sitboard/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/IZZY-Agency/sitboard/ci.yml?branch=main&amp;label=CI" alt="CI"></a>
  <a href="https://github.com/IZZY-Agency/sitboard/releases/latest"><img src="https://img.shields.io/github/v/release/IZZY-Agency/sitboard?label=release&amp;color=f5c542" alt="Latest release"></a>
  <a href="https://github.com/IZZY-Agency/homebrew-tap"><img src="https://img.shields.io/badge/brew-izzy--agency%2Ftap%2Fsitboard-f5c542" alt="Homebrew formula"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/IZZY-Agency/sitboard?color=f5c542" alt="MIT licence"></a>
</p>

# sitboard

A live board of the Claude Code and Codex terminal sessions running on your machine.

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
  Opus, Sonnet, Haiku; a Codex model without a family colour) with its effort level (low
  to max, or the word Codex recorded, such as "ultra"; for Claude Code, before
  the first reply, the level it was started with, marked "at launch"), how long
  it has been in that state, its output tokens and, for Claude Code, a cost
  estimate, when known, the pull requests and merge requests it opened with
  their checks and review, and, on the Board, links to the claude.ai artifacts
  it published. The pull request, merge request and issue numbers shown on the
  Board, in Lanes and in Teams are links to GitHub or GitLab.
- **GitLab merge requests.** A merge request a session created with
  `glab mr create`, or with `git push -o merge_request.create`, shows like a
  pull request: `!12`, with its state, the issues it closes where GitLab
  gives them, and, for an open request, its draft flag, pipeline and approval
  ("approved" or "approval needed"). Catch-up lists it as opened and merged. On gitlab.com and self-hosted GitLab, for the hosts your own
  `glab` is logged in to. Once
  a merge request is on the board, the page says "pull or merge request".
  Off with `--no-gitlab`.
- **Codex sessions.** Sessions of the Codex terminal client and of `codex exec`
  get cards like Claude Code's. A Codex session that waits for your approval
  shows as blocked only with agterm. Threads of the Codex desktop app and of
  editor plugins are not shown.
- **A critter for every agent.** Each agent gets its own small creature,
  drawn from its session id, which helps tell agents apart; its eyes, outline
  and the line under its feet show the state, and the column and words still
  say it. Critters move only while you can see them, and with reduced motion
  they stay still.
- **Board** (the default view). One column per state, in this order: Needs
  you, Working, With coordinator, In review, Done · 24h, Idle.
- **Lanes.** One row per project, with a "Needs you" strip on top.
- **Teams.** Your queue beside one panel per coordinator with its workers, and
  one panel of solo agents.
- **Project tiles or a side rail.** You choose which; they pick the projects
  every view shows. A project that spans several repositories shows as one
  entry with its parts. A grouping you do not want can be forgotten once
  (that folder stays apart until no agent has sat in it for two minutes) or
  set to never group; a grouping you want to keep can be set to always group.
  The status bar lists every folder with a setting, to set it back to auto.
- **Catch-up.** A panel with the finished turns, pull and merge requests
  opened and merged, and artifacts published since you last pressed "Caught up", up to
  7 days back, grouped by project. Opening it marks nothing read. It covers
  the Claude Code sessions that ran in the last 7 days, including those that
  had ended before sitboard started, and the Codex terminal sessions sitboard
  followed; while the Claude Code ones are still being read, the panel says
  so.
- **A tap on the shoulder.** The tab's title and icon show how many agents
  need you, and the icon a "?" when the board is not live. Turn on Notify in
  the header for a desktop notification when an agent starts needing you; for
  one agent it says why, for several how many, never which agent or task, and
  it stays quiet while the board is in front of you. The switch is off until
  you turn it on, and the choice holds for every tab of that board's address
  in that browser (sitboard on another port asks again).
- **Spend.** A Spend button in the header gives today's use and opens a panel
  with each project's use per day for the last 7 days: output tokens and, for
  Claude Code, a cost estimate at API list price, from Claude Code's own price
  table or the total Claude Code recorded, always marked "≈" and
  never a bill. Codex keeps no price, so its sessions show tokens only.
- **Leftovers.** A quiet "N leftovers" link in the status bar lists merged
  work still on the machine that sitboard has evidence for: a worktree, branch
  or branch on GitHub whose pull request merged into the default branch, and
  idle sessions whose own pull requests all merged at least 24 hours ago. Each
  item links its merged pull request and says what sitboard checked, for
  example "merged · nothing uncommitted · not in use".
  Work that still holds something (commits after the merge, uncommitted
  files, a stash, a process in the folder) is only counted, except a
  worktree in use only by finished sessions that are themselves listed, shown as
  "in use by its session" (or "by a finished session"). It is a list
  only. GitHub only; off with `--no-github`.
- **A jump to the terminal.** With [agterm](https://github.com/umputun/agterm),
  one click jumps to the session's terminal, and cards get better titles; a
  Claude Code session in tmux jumps to its tmux pane instead, when a tmux client
  is attached, with or without agterm. Otherwise,
  clicking a card shows the session's folder and name to copy.

## Privacy

- **Local only.** sitboard listens on `127.0.0.1`, never on your network.
- **A private link for this run.** It prints a link that carries a secret, new each time sitboard starts; your browser swaps it for a cookie. Requests without it, and requests from other websites, are refused.
- **Nothing leaves your machine**, except what your own `gh` and `glab` commands send:
  - to GitHub, a check that `gh` is logged in, and read-only lookups for pull request state. For Leftovers, these lookups include up to 100 branch names from each clone on the board: its local branches (never-pushed ones included) first, then its origin branches. `gh` runs with your environment and its own login; sitboard never reads or stores a GitHub token.
  - to GitLab, through `glab`'s own login, and only about a GitLab host that a session inside `--roots` points to (the host of a merge request it created or a push of its named, or of its folder's origin when that is not on github.com): `glab auth status`, glab's own login check, which contacts the host when glab has a login stored for it; and read-only lookups for merge request state, only to a host whose login check passed within the last ten minutes (gitlab.com included), which send the project's path or numeric id and the numbers of the merge requests a session created or a push of its named, and ask which issues such a merge request closes. No branch name goes to GitLab.
- **No network traffic needs both `--no-github` and `--no-gitlab`.** With both, sitboard runs no `gh` and no `glab` and makes no network calls. With `--no-github` alone, `glab` can still contact the GitLab hosts your sessions use.
- **glab.** sitboard runs your own `glab` (found on your `PATH` or in the usual install folders); with glab not installed it runs none. `glab version` runs when sitboard starts (and each minute while it fails); while a session needs a host that is not github.com, `glab auth status` runs for it about once a minute until the check passes, then about every eight minutes. For a host glab is logged in to, sitboard also runs `glab config get --host <host> subfolder`, which reads glab's own config. glab runs in `/` with an environment sitboard builds, not your shell's: `PATH=/usr/bin:/bin`, `HOME`, `USER` and `LOGNAME` from the system's user list, the keyring's `DBUS_SESSION_BUS_ADDRESS` and `XDG_RUNTIME_DIR`, the proxy variables `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` (and their lowercase forms; `ALL_PROXY` is not passed), so a proxy you set sees which hosts glab talks to, and settings that turn off glab's telemetry, update checks, prompts, debug output and helper downloads. No token, host or config folder from your environment reaches glab. sitboard never reads glab's config file or a token itself, and glab's output is read for its answer and never logged.
- **What it reads.** Claude Code's session files, the transcripts of running sessions and, at start, those changed in the last 8 days, including ended sessions, to derive states, times, models, effort levels, token counts, Claude Code's own cost estimates, pull requests, artifacts and names. It keeps only those derived facts, in memory. From `~/.claude.json` it reads only Claude Code's price table (`additionalModelCostsCache`); every other key in that file is read past and not kept. When it first sees a session in a running `claude` process, it reads the process's arguments and environment (on macOS through the kernel's `kern.procargs2` query, on Linux from `/proc/<pid>/cmdline` and `/proc/<pid>/environ`) and keeps three facts: the agterm session id from `AGTERM_SESSION_ID`, the tmux pane and its server's socket path from `TMUX_PANE` and `TMUX`, and the effort level the session was started with, from the `--effort` flag or `CLAUDE_CODE_EFFORT_LEVEL`, as one of the five level words. Every other argument and variable is read past and not kept, logged or served. It also asks `git` about each session's folder (repository, branch and remote) and, for Leftovers, about the worktrees, local and origin branches and stashes of the repositories on the board and the local status of the worktrees that could be leftovers and of a main checkout where an idle session sits (read only: no fetch, no lock), every 10 minutes, or sooner when the board sees a merge or a listed session changes (at most once a minute); `ps` for when the `claude` and `codex` processes started; `lsof` (on Linux `/proc`) for each of your processes' parent and working folder, kept in memory for that look only; and, with agterm, `agtermctl` for its windows, workspaces, sessions and their events. Stash messages, file names from `git status`, which process works where and commit ids stay in memory: they are not served, written to disk or logged.
- **tmux.** For a Claude Code session in tmux, sitboard asks `ps` for the terminal of the session's process, and asks `tmux` once, on that pane's own socket and never starting a server, whether the pane shows that terminal; only then does the card offer the tmux jump. Before talking to a tmux socket it checks that the socket is yours, in a folder of yours that nobody else can write to (a socket directly in a shared folder such as `/tmp` gets no tmux jump), and it runs `tmux` by the absolute path it found at start (in a folder owned by you or root that nobody else can write to), giving it nothing from its environment but `PATH`. It keeps the pane's terminal and the session process's id and start time in memory, never on disk, to check them again when you click. When you jump, it asks `tmux` for that server's panes and clients, and reads the arguments and environment of the tmux client it is about to move, keeping only its agterm session id (none when that client itself runs in tmux). The pane and the socket never reach the page.
- **Codex.** If Codex's folder exists (`~/.codex`, or `$CODEX_HOME`), sitboard reads the rollout files under its `sessions` folder: the first line of the recent ones (by modification time) and of those whose lock is held, to tell which kind of session it is; the rollouts of running and recently active sessions, for states, times, models, effort levels, pull requests, the sessions they started and token counts; and, at start, those of the last 7 days, including sessions that had already ended, for their folder and token counts. It reads Codex's session index for each session's title, and lists the names of Codex's lock files: it asks `lsof` (on Linux `/proc/<pid>/fd`) which process has a session's lock file open, without taking the lock. It finds the Codex terminal clients by their command line in `ps`, reads their environment, keeping only the agterm session id and whether `CODEX_HOME` is the folder it watches, and asks for their working folder when the command line does not give it; it drops what it read when it cannot confirm the process is still the same one. It never opens Codex's databases and writes nothing in Codex's folder. It keeps only the derived facts, in memory; Codex's prompts, replies and tool output are never kept or shown.
- **What it writes.** Only its own state file (saved through a temporary file beside it) and that file's lock file (creating their folder if needed). The state file (`~/Library/Application Support/sitboard/state.json` on macOS; on Linux `$XDG_STATE_HOME/sitboard/state.json`, by default `~/.local/state/sitboard/state.json`; or the file given with `--state`) keeps which cards you have seen, the catch-up marker, the folders that group projects, the groupings you forgot, the folders you set to always or never group, and, for catch-up items still unread behind the marker, a hash of each item's identity (not the identity itself) and its time. The lock file holds only the running board's process number and machine name, and a clean stop empties it. With `--no-state` it writes neither.
- **No prompts, code or tool output.** The page gets session names and titles, home-relative folder paths (including the folders you set to always or never group, up to 200, whether or not an agent works in them), repository names with their owner ("owner/name") or GitLab group path, agterm workspace names, pull request and merge request numbers, titles and links with their state, checks, review or approval and draft flag, the numbers and links of the issues they close in their own repository or project, the GitLab status line, the links and titles of the artifacts a session published, and, for Leftovers, the home-relative worktree and clone paths and branch names of merged work still on the machine, counts of held-back items by reason and when that list was made, plus derived fields such as states, times, model ids and effort levels, token counts and the estimated cost at API list price, subagent counts, the versions of the tools it reads, and sitboard's own version, commit, operating system and architecture. A session's name is the one Claude Code records for it or, when it has none, its agterm terminal's name. A Codex session's name is the title Codex recorded for it, which the model wrote from the conversation (or the one you gave it), cleaned and shortened. A session's title is its open pull or merge request's title or, with agterm, the short title its terminal shows there (for Claude Code, its own summary of the task); without either, it is the session's name, its terminal's name or its folder's name. Prompt text, code and tool output are never shown and never served. The only free text the page gets from a transcript is an artifact's title (the title its own publish reported) and, when the session's own record has no name (as for a session that has ended), the name Claude Code recorded for it in the transcript (yours, or one it made itself, for example from a plan), both cleaned and shortened. The GitLab hostnames of your projects' origins and of the merge requests, and the host named in the GitLab status line, appear on the page. From a Codex rollout the page gets only derived fields (model, effort, folder, pull requests, token counts), no text from the conversation; the only free text from Codex's files is the session's title.
- **Desktop notifications** are off until you turn them on. The browser shows them from the page, on this machine, and they never name an agent, a project or a task. The switch, and which notifications were already shown or skipped (by card, reason and time), are kept in that browser's storage for the board's address, as are the page's view settings.
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

Flags: `--port`, `--no-open`, `--no-github`, `--no-gitlab` (with `--no-github`: no network traffic), `--roots` (only agents working under these folders), `--stale-after` (default `20m`), `--state <file>` (use this state file instead of the default one), `--no-state` (keep seen marks, the catch-up marker and groupings in memory only), `--version`, `--licenses` (sitboard's licence and the third-party notices).

One state file has one board: while sitboard runs, a second sitboard (0.3.0 or later) on the same state file refuses to start. Give a second board its own file with `--state <file>`, or use `--no-state` for a throwaway run that saves nothing. The lock is a `<state file>.lock` file beside the state file, which holds the running board's process number and machine name; a clean stop empties it. On a folder that does not support locks, sitboard warns and runs without the lock.

## Requirements

- [Claude Code](https://claude.com/claude-code), [Codex](https://github.com/openai/codex) or both: sitboard shows their sessions.
- Optional: [`gh`](https://cli.github.com), logged in, for pull request state and Leftovers.
- Optional: [`glab`](https://gitlab.com/gitlab-org/cli) (tested with 1.131.0), logged in with `glab auth login` to each GitLab host, gitlab.com included, for GitLab merge requests.
- Optional: [agterm](https://github.com/umputun/agterm), for the one-click jump to a session's terminal and better card titles.
- Optional: tmux, for the same one-click jump to a session running in a tmux pane.

## Not there yet

- Removing leftovers: sitboard lists them, but removes nothing.
- GitLab in Leftovers: Leftovers cover GitHub only.
- GitLab under a subfolder of its host (such as `example.com/gitlab`).
- Approval on GitLab Community Edition, which has no approval fields.
- In Catch-up: `codex exec` runs, and Codex sessions known only from the start-up read for Spend. A cost estimate for Codex (Codex keeps no price).
- Threads of the Codex desktop app and of editor plugins.

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
