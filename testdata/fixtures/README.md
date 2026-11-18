# Test fixtures

Fixtures are real record shapes with every free-text value replaced. They are written by hand from the record shapes in the design doc (section 13). No fixture, golden file, test or doc may hold a real project, client, workspace, session or account name, a home path or a token. `scripts/check-private.sh` enforces this.

## Placeholder allow-list

Use only these values:

| What | Placeholders |
|---|---|
| Agent and session names | `coordinator-a`, `worker-a` … `worker-f`, `solo-a` |
| Repository | `example-org/example-repo` |
| Folders | `/home/dev/projects/alpha`, `/home/dev/projects/beta`, `/home/dev/projects/gamma` (home-relative on the page: `~/projects/alpha`) |
| Pull request URL | `https://github.com/example-org/example-repo/pull/101` (other numbers allowed) |
| Artifact URL | `https://claude.ai/artifact/exampleArtifact0001` (other digits allowed); an artifact title starts with `Placeholder` |
| Session ids | UUID-shaped values made of a repeated digit, for example `11111111-1111-4111-8111-111111111111` |
| Model ids | as they appear in real records |

Keep as they are: keys, enum values, timestamps, numbers and tool names an adapter parses. Replace every other string.

## Layout

`testdata/fixtures/<source>/<tool-version>/<case>.<ext>`, for example `testdata/fixtures/claude/2.1.326/session-busy.json`. Each source stream owns its own folder.
