# glab 1.131.0 fixtures

Answers of `glab api` as GitLab gives them, with every free-text value replaced by a placeholder (see `../../README.md`). Key names, value types, enum values and nesting were copied from real anonymous answers of gitlab.com for a public project (glab 1.131.0, 20 Nov 2026); numbers, titles, branches and addresses are placeholders.

| File | What it stands for |
|---|---|
| `lookup.json` | The GraphQL query of `buildQuery`: merge requests asked by number (`m`) and by source branch (`b`). A merged merge request answers `closedAt: null` and `mergedAt` set; `iid` is a string; `headPipeline` is `null` when there is none; a fork's merge request has `sourceProjectId` ≠ `projectId`. 106 has a state value the adapter does not know. |
| `partial-errors.json` | An answer with both `errors` and `data` (glab exits 1 and prints it). |
| `project-null.json` | A project that is gone or not visible to the account. |
| `complexity-error.json` | A query over GitLab's complexity limit: errors, no data. |
| `no-approval-fields.json` | A Community Edition instance refusing the approval counts: `approvalsLeft` and `approvalsRequired` are Enterprise-only fields (`ee/`), so CE answers graphql-ruby's "Field '…' doesn't exist on type 'MergeRequest'" for each, with no data. Written from graphql-ruby's message format and GitLab's error extensions, not captured from an instance. |
| `closes-issues.json` | REST `projects/:id/merge_requests/:iid/closes_issues`, full key set; 9 is in another project, 7 is repeated. gitlab.com answers `web_url` under `/-/work_items/`. |
| `open-issues.json` | The GraphQL query of `buildIssuesQuery` for four projects, from a real anonymous answer of gitlab.com (27 Nov 2026) with titles, labels, milestones, numbers and addresses replaced: one with issues, one asked in another case than GitLab writes its path (GitLab answers the path as written there), one with issues turned off (`issuesEnabled: false` with an empty `issues` connection, not null), and one that does not exist (`null`, no error). `currentUser` is `null`, as gitlab.com answers it anonymously (no error); with a login it is the account's address, `{"webUrl":"https://<host>[:port]/<mount>/<username>"}` (the shape of GitLab's users API, and of gitlab.com's answer for a public account), which says which instance answered. A milestone's `dueDate` is a day, though the schema calls it a time; `iid` is a string. |
| `version.txt` | `glab version` output. |

## Measured on gitlab.com

GraphQL complexity limit: 200 anonymous, 250 logged in. `buildQuery` with 50 numbers and 50 branch names (the caps) scored **148**, read from `queryComplexity{score limit}` on gitlab.com. `buildIssuesQuery` scored **5 + 19 a project** (8 projects: 157; 10: 195; 27 Nov 2026); without `currentUser{webUrl}` it was 3 + 19. Failures seen: `glab: 404 Project Not Found (HTTP 404)` (REST, exit 1); `{"errors":[{"message":"Invalid token"}]}` with `glab: Invalid token` (exit 1); `{"data":{"project":null}}` for a project that does not exist (exit 0).
