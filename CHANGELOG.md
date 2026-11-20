# Changelog

## 0.3.0

Catch-up, a critter for every agent, effort levels, links on cards, a tap on the shoulder when an agent needs you, and grouping you can set.

What is new:

- Catch-up. The Catch-up button in the header says how many items are new and opens a panel over the current view: what happened since you last pressed "Caught up", up to 7 days back, grouped by project. It lists finished turns, pull requests opened and merged, and artifacts published. Opening it marks nothing read; "Caught up" does. It covers the Claude Code sessions that ran in the last 7 days, including those that had already ended before sitboard started, which it finds by their transcripts; while it is still reading those, the panel says so.
- Every agent has a critter: a small creature drawn from its session, the same in every view and after a restart. Its eyes, its outline and the line under its feet show the agent's state. It takes the place of the state dot on cards, in the needs-you queue, in Teams rows and in Lanes; the columns and words still say the state. Critters move only while you can see them, and with reduced motion they stay still.
- Cards show the session's effort level (low, medium, high, xhigh or max) next to its model, on the Board, in Lanes and in Teams: the level the session's latest reply ran at or, before its first reply, the level it was started with, marked "at launch". From high up the level's box is filled, deeper at each step, so high, xhigh and max stand out; a level set at launch is not filled, and on the Board its box is dashed.
- A tap on the shoulder. The tab's title ("(3) sitboard") and its icon show how many agents need you, and the icon shows a "?" when the board is not live. Turn on Notify in the header for a desktop notification when an agent starts needing you: for one agent it says why, for several how many, and it never names an agent, a project or a task. It stays quiet while the board is in front of you. The switch is off until you turn it on, and the choice holds for every tab of that board's address in that browser.
- Pull requests on a card, and the issues they close, are links to GitHub, opened in a new tab without a referrer. Their numbers are underlined and carry a small ↗, as do the pull request links in Catch-up, so a link is visible without hovering.
- Grouping settings. A project with parts can be set to "Always group", so its folder keeps grouping, and the "Forget grouping" confirm now offers "Forget once" or "Never group". The status bar says when folders have a setting and opens their list, where each can be set back to auto.
- `--state <file>` gives a board its own state file, and `--no-state` keeps seen marks, the catch-up marker and groupings in memory only, for a run that saves nothing.

What changed:

- One state file, one board. A second sitboard on the same state file now refuses to start (exit code 1). Until now it started a second board, and the two silently overwrote each other's seen marks and groupings. Give the second one its own file with `--state <file>`, or run it with `--no-state`. A lock file, `state.json.lock`, now sits beside the state file: a running sitboard writes its process number and machine name there, and a clean stop empties it. On a folder that does not support locks, sitboard warns and runs without the lock. sitboard 0.2.0 and older take no lock, so they are not stopped; when another program, such as an older sitboard, has changed the state file, the board warns once before its next write replaces those changes.
- sitboard refuses to start when the state file cannot be used (a folder it cannot write to, or a file that is not a sitboard state file), instead of failing at the first save or replacing another program's file.
- A card's pull request lines are no longer part of its jump button: a click on a pull request opens it.
- Haiku's model colour is a brighter lime.
- sitboard now reads the transcripts of the Claude Code sessions that ran in the last 7 days, not only of running ones, to find the sessions that had ended before it started. It keeps only the facts it derives from them, in memory.
- When it first sees a session in a running `claude` process, sitboard reads the process's arguments and environment, as before for the agterm session id. It now keeps a second fact from them: the effort level the session was started with (the `--effort` flag or `CLAUDE_CODE_EFFORT_LEVEL`), as one of the five level words. On Linux it now also reads `/proc/<pid>/cmdline` for this. Every other argument and variable is read past and not kept, logged or served.
- The page now gets a second piece of free text from a transcript: when the session's own record has no name (as for a session that has ended), the name Claude Code recorded for it in the transcript (yours, or one it made itself, for example from a plan), cleaned and shortened. When the record has the name, the page got it from there already. The page also gets the effort levels, and the home-relative paths of the folders you set to always or never group (up to 200), whether or not an agent works in them.
- The state file also keeps the folders you set to always or never group, and, for catch-up items that are still unread although they happened before the moment you marked, a hash of each item's identity (not the identity itself) and its time, at most 1000. A state file that holds a grouping setting is written as version 2; one without stays version 1. Upgrading needs nothing: 0.3.0 reads the files of 0.2.0 and 0.1.0 as they are.

Limits:

- Links on cards are on the Board only, not yet in Lanes or Teams.
- Catch-up does not cover: sessions that ran more than 7 days ago; sessions whose transcript Claude Code has already removed; `claude -p`, SDK and MCP-server runs; and, after a restart, something that happened before the moment you marked but that sitboard learned only after the restart (say, a merge GitHub reported late). What it had learned before stopping stays unread, up to 1000 items; after a crash, what arrived in its last 10 seconds may not. Of the repositories known only from sessions that had already ended, GitHub is asked about the 20 newest; pull requests in the others are not listed. With `--no-state`, Catch-up lists the last 7 days again after every start.
- The effort level "at launch" can differ from the one the first reply runs at: when `CLAUDE_CODE_EFFORT_LEVEL` is set in a Claude Code settings file, or when the model cannot run that level.
- Desktop notifications need your browser's permission; sitboard on another port asks again.
- If you go back to 0.2.0 or 0.1.0 after setting a folder to always or never group, it refuses to start: it says the state file "was written by a newer sitboard (file version 2, this build reads up to 1)" and leaves the file as it is. Before going back, set every folder back to auto (the file is then version 1 again), or move the state file aside. A version 1 file opens in them as before; on their next write they drop the catch-up items still unread behind the marker, which then count as read.

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
