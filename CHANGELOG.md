# Changelog

## 0.1.0

The first release: a live board of the Claude Code sessions running on your machine.

Run `sitboard` and it opens the board in your browser. It finds the sessions by itself; nobody creates tasks, and the agents need no setup.

What it shows:

- A card per live session, with its name or title, its project, its status (working, blocked on a question or a permission, finished), the model, and how long it has been in that state.
- The columns, in board order: Needs you, Working, With coordinator, In review, Done · 24h and Idle. Working that has gone quiet for too long is flagged as stale (`--stale-after`, 20 minutes by default).
- Who spawned whom: a coordinator's workers sit under it, worked out from the sessions' own records.
- The pull requests a session opened, with their state, checks, review decision and the issues they close, read through your own `gh`, read-only.
- Three views: Board (one column per state), Lanes (one row per project, with a "Needs you" strip on top) and Teams (your queue beside one panel per coordinator with its workers).
- Project tiles or a side rail (switchable) choose which projects every view shows; a project that spans several repositories shows as one entry with its parts.
- Mark a finished card as seen. With the agterm terminal, jump to a session's terminal in one click, get better card titles, and mark a session seen by selecting it in agterm. Without agterm, clicking a card shows the session's folder and name to copy.
- The page gets names, titles, folder paths, states and pull request links, never session text.
- `sitboard --licenses` prints sitboard's licence and the third-party notices; the page links to them.

Not in this release yet:

- Codex sessions: only Claude Code sessions are shown.
- Catch-up: what happened since you last looked.
- Leftovers: merged work whose worktree, branch or session still exists.

Tested on macOS. The Linux builds are made and checked, but not tested by us on a Linux desktop.
