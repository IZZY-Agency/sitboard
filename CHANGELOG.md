# Changelog

## 0.2.0

Model names in colour, links to the artifacts a session published, and a way to undo a project grouping.

What is new:

- Cards on the Board link the artifacts their session, or one of its in-process subagents, published on claude.ai, newest first, under its pull requests. A click opens the artifact in your browser. When there are more than three, the card shows the two newest and "+N more".
- Forget a project grouping. Select a project with parts and press "Forget grouping" (in the tiles, at the end of its parts row; in the rail, under its parts), then confirm. Its parts are no longer grouped under it, at once, and the folder does not group again until no agent has sat in it for two minutes. A grouping that forms after that is new, for example when two sessions are opened in that folder again, and can be forgotten again.

What changed:

- Claude model names show in their family's colour (Fable, Opus, Sonnet, Haiku) on Board cards, in Teams and in Lanes. In Lanes, a line that needs you or has failing checks keeps that line's colour instead. A model from another maker, or a Claude family sitboard does not know, gets no family colour; a card with no model says "model unknown" in a dashed, muted chip.
- The page now gets one piece of free text from a session's transcript: the title of each artifact the session published, as its own publish reported it, cleaned and shortened. Until now only identifiers, session names and times were taken from a transcript: the model id, the working folder, the pull requests the session opened, the names and terminal ids of the sessions it started, and the names of the sessions it heard from. Prompt text, code and tool output are still never shown and never served.
- sitboard's state file (`~/Library/Application Support/sitboard/state.json` on macOS; on Linux `$XDG_STATE_HOME/sitboard/state.json`, by default `~/.local/state/sitboard/state.json`) kept which cards you marked seen and which folders group projects. It now also keeps the folders whose grouping you forgot, so a restart does not group them again.

Limits:

- Artifacts: only the ones a session, or one of its in-process subagents, published itself (an update of another session's artifact counts as a publish). A rename made later elsewhere is not seen: the card shows the title from the session's latest publish. A deleted artifact stays listed. Claude Code sessions only, and on Board cards only, not in Lanes or Teams.
- If you go back to 0.1.0, it drops the forgotten groupings from its file on its next write, and those folders may group again.

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
