# Maintainer scripts

The working repository is private. The public repository,
`github.com/IZZY-Agency/sitboard`, receives snapshots exported by
`publish.sh`, and releases are built from the public repository.

| Script | What it does |
|---|---|
| `check-private.sh` | Fails when private material is in the tree, the history or a text file. `--help` lists the modes. |
| `make-denylist.sh` | Builds the private deny-list of names that `check-private.sh` uses, outside the repository. |
| `publish.sh` | Exports the allowlisted tracked files into a checkout of the public repository and runs the gates; commits only with `--commit` or `--update`. |
| `package.sh` | Builds the four release archives (the binary with `LICENSE`, `THIRD_PARTY_NOTICES.md` and `README.md`) and their `.sha256` files (`make release`). |
| `notarize.sh` | Notarizes a Developer ID-signed macOS binary; `package.sh` calls it. |
| `verify-release.sh` | Checks the archives and runs those this machine can run (`make verify-release`). |
| `render-formula.sh`, `formula/sitboard.rb.tmpl` | The Homebrew formula for one release. |
| `release.sh` | Builds, verifies and publishes the GitHub release from the public repository at the tag. A dry run unless given `--go`. |
| `tap.sh` | Writes the formula into the tap checkout from the published release, and commits. |
| `*_test.sh` | The tests of these scripts; `make check` runs them. |

`make check` also runs the export's gates at the pull request (`make export-gates`), so a refused file kind (`KINDS`), an oversized or private file, an ignored tracked file, or a pointer to `docs/` or the website's folder in a code comment fails there and not at release time. It runs `PUBLISH_GATES_ONLY=1 scripts/publish.sh`: no target at all (no public checkout, no new repository, nothing probed or written), no network (git may not fetch a missing object), no commit. The tracked files are staged in publish.sh's own scratch repository, the same gates run, and `check-private.sh` runs from the staged copy, with the deny-list, or with `--no-denylist` when `PRIVATE_FLAGS` is exactly that (any other value is refused: `--help` would make the scanner exit 0 unscanned). A search that cannot finish (a grep or sed error) fails the gate, in a real export as well. Only the staged snapshot decides: its `.gitignore` and its `check-private.sh`, so an edit that is only in the worktree neither passes nor refuses it. The tests set `PRIVATE_FLAGS` per case, so `make check PRIVATE_FLAGS=--no-denylist` (CI, and the release's clean copy) does not weaken them. The clean-copy `make check` and `make build` stay release-only (they would run this step again); when the release runs that clean copy, whose own `make check` runs this step, the rehearsal switches (`PUBLISH_TEST_*`, `PUBLISH_GATES_ONLY`, `PUBLISH_IDENT`) do not reach it.

publish.sh refuses an exported path with a control character (a newline, a tab), keeps everything it writes in one scratch folder under `TMPDIR` (made absolute, and named by an explicit template: macOS `mktemp -d` alone ignores `TMPDIR`), gives every `check-private.sh` run and the clean copy a `TMPDIR` inside it, and removes it on exit, Ctrl-C or SIGTERM (its traps are set before the folder exists). An interrupted write is undone in two phases: the paths the write had begun (recorded as each one starts) are removed, never through a link on the way, and then the old index and files are checked out; the removal is never replayed once the restore has started. If either phase fails, it says so, prunes nothing, keeps that folder and writes `recover.sh` there: `bash <folder>/recover.sh` ignores the calling shell's git environment, refuses unless the target is the same folder and repository (path and inode), and finishes only the phases not yet done. A gates-only run reads `RELEASE_VERSION` from the staged Makefile.

The release tests run on macOS through `make check`. To run them on Linux as well, commit first, then:

```sh
make release-test-linux
```

It clones what is committed into a temporary folder and runs `make release-test` in a `golang:1.27` container (another image: `make release-test-linux LINUX_IMAGE=…`). A plain clone is needed because a worktree's `.git` file points at a host path the container cannot see. Uncommitted changes are not tested, and the target says so when there are any.

## What each private-material scan reads

| Command | Reads | Run by |
|---|---|---|
| `check-private.sh` | the work tree: every tracked or untracked, not ignored, file, and what is staged | `make check`, and `publish.sh` on the tree it exports |
| `check-private.sh --history --scope=branch` | everything reachable from HEAD, the local `main`, `origin/main` and every tag (tag names and annotated tag messages too), each back to its root, and those ref names. Other branches, the stash, notes and other refs are left out; its summary line names its scope and says how many refs it left out | `make check` |
| `check-private.sh --history` | everything reachable from every ref, and every ref name | `make private-all` on this repository, by hand; `publish.sh`'s gate on the public repository |
| `check-private.sh --history --pull-refs` | the same, and the heads of origin's pull requests | you, before going public (below) |

`make check` scans only the branch in hand, so another worktree's unfinished branch in the same repository cannot fail it. A scoped clean result is not the full one.

There is one release gate, in `publish.sh`, and its history scan runs on the public repository after the commit, so it never reads this repository's other branches. The full scan of this private repository is run by hand: `make private-all` (the tree and `--history` with the deny-list) before every release, and `--history --pull-refs` before going public.

## Releasing a version

The version lives in one place: `RELEASE_VERSION` in the Makefile. `CHANGELOG.md` must open with its `## <version>` entry; `make check` enforces that. Every script below ignores your global and system git config and hooks, and none of them pushes.

Before step 1, run **`make audit`**: govulncheck on the Go code and `npm audit` on the packages of the web page and of the website (it needs the network, so `make check` does not run it). A finding stops the release until the dependency, or Go itself, is updated.

1. **Export.** In a clean private tree, with a clone of the public repository next to it (its origin exactly `https://github.com/IZZY-Agency/sitboard.git`):

   ```sh
   scripts/publish.sh ../sitboard-public                 # stage, run the gates, show the tree
   PUBLISH_IDENT="Name <email>" scripts/publish.sh ../sitboard-public --commit
   ```

   It refuses before touching anything when:
   - the export set holds a `.claude`, `.codex` or `docs` folder, the website's top-level folder, a `.env*`, `*.pem`, `*.key`, `CLAUDE.md` or `AGENTS.md` file, or a tracked file that `.gitignore` ignores (the website is not published here; of `.github`, two files are exported, each by name: the workflow `ci.yml` and the README's banner `sitboard-banner.svg`, so the website's workflow never is);
   - an allowlisted path below the top level is not exactly one tracked file (a folder of that name would export what is inside it);
   - an exported `.github` SVG is not plain drawing only, checked with line breaks and tabs read as spaces: an element other than `svg`, `g`, `rect`, `path`, `text` and `tspan` (`SVG_TAGS`), an attribute not used by the banner itself (`SVG_ATTRS`, exact spelling) or without a quoted value, or, ignoring case, any `<!` (doctype, entity, comment, CDATA), `<?`, `&`, `url(`, event attribute (`on…=`), prefixed attribute (`xmlns:x=`, `xlink:href=`), `href`, `data:`, `style`, script, image, `foreignObject`, metadata, or Inkscape or Sodipodi residue (`SVG_REFUSED` in `publish.sh`);
   - the target is, contains or sits inside this repository (by file identity), shares its git folder, or holds its history;
   - the target has uncommitted changes or stray files.

   The gates on the snapshot:
   - the staged files are exactly the exported ones, blob for blob;
   - no pointer to an unexported `docs/` file, and no reference to the website's folder (a link to https://sitboard.com is fine);
   - `check-private.sh` with the deny-list;
   - `make check PRIVATE_FLAGS=--no-denylist` and `make build` in a clean copy;
   - after committing, `check-private.sh --history` with the deny-list (the commit message too), and the commit is undone if that fails.

   `--commit` commits "sitboard <version>" with the changelog entry and tags `v<version>`; `--update "<subject>"` commits between releases without a tag. Both mark the exported private commit with the local tag `last-published` and print the push command, which names the new commit by its id.

2. **Build and publish the release.** On a Mac, with the Developer ID identity and a notarytool profile (supplied at release time, never stored here):

   ```sh
   SIGN_IDENTITY="Developer ID Application: <team>" NOTARY_PROFILE=<profile> scripts/release.sh        # dry run: checks, plan, notes
   SIGN_IDENTITY="Developer ID Application: <team>" NOTARY_PROFILE=<profile> scripts/release.sh --go   # do it
   ```

   - **Refusals:** unless `v<version>` is on the public repository and no release of it exists.
   - **Build:** in a clone of the public repository at the tag, `make release`, which builds only there (signed and notarized).
   - **Verify:** `REQUIRE_SIGNATURE=1 make verify-release VERIFY_FLAGS=--rebuild` checks the signatures, notarization and the tag's commit in `--version`, and that a second build has the same bytes. The macOS archives are left out of that byte comparison, because a signature changes them. Set `EXPECTED_TEAM_ID` to also check the team.
   - **Publish:** it copies the eight files to `bin/release/<version>/` here and runs `gh release create v<version> ... --verify-tag` with the changelog entry as notes.

   `PACKAGE_DEV=1 make release` makes a local test build anywhere; it is never a release.

3. **Tap.** First make sure `https://sitboard.com`, the formula's homepage, is live: the tap is not pushed before it is. Then, with the eight files in `bin/release/<version>/` (so `tap.sh` can compare checksums):

   ```sh
   PUBLISH_IDENT="Name <email>" scripts/tap.sh ../homebrew-tap
   ```

   It downloads the published archives and refuses them unless they match both the published and the local `.sha256`. Then it writes `Formula/sitboard.rb`, adds the README lines, runs `brew style` (on failure it puts the checkout back as it was) and commits. It prints the push command with the exact commit id.

## Going public

Before the repository or its history is published, run the history scan once more with the deny-list. Add the heads of every pull request, because GitHub keeps them under `refs/pull/N/head` even when no branch reaches them any more:

```sh
scripts/check-private.sh --history --pull-refs
```

`--pull-refs` fetches origin's `refs/pull/*/head` into `refs/check-private/pull/` for the run, says how many it fetched, and removes those refs afterwards. Run it in a full clone: the history scan refuses a shallow one.

No local scan can see everything GitHub still serves: commits that a force-push replaced, or that only deleted branches reached, can stay fetchable by commit id. That is one reason to publish a fresh snapshot repository, as `publish.sh` does, instead of making the working repository public.
