# gh 2.106.0 fixtures

Answers of `gh api graphql` as GitHub gives them, with every free-text value replaced by a placeholder (see `../../README.md`). Key names, value types, enum values, nesting and the error entries were copied from a real answer of github.com for a public repository (gh 2.106.0, 27 Nov 2026); numbers, titles, labels and milestones are placeholders.

| File | What it stands for |
|---|---|
| `open-issues-pinned.json` | The query of `buildIssuesQuery` for two repositories, the first with four pinned numbers asked as `i<number>:issue(number:)` aliases inside its `repository` alias: 301 is open (`"state": "OPEN"`), 302 closed (`"CLOSED"`), 303 is a pull request's number and 99999999 no number at all. Both of the last two are `null`, each with a `NOT_FOUND` error whose `path` is `["r0", "i<number>"]` and message "Could not resolve to an Issue with the number of …". The repository's own answer stands beside them. gh exits 1 and prints the answer when it holds errors. |
