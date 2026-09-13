# Syncing official CLIProxyAPI

This fork keeps a rebase queue shape: `main` is an upstream release tag plus
one commit per fork change, with no merge commits. Branch protection enforces
it mechanically — direct pushes and merge commits are rejected, so every
change lands as one squash-merged PR rebased onto the current base.

Track **stable release tags** (`v*`). `origin/main` may move past the latest
tag with unfinished work; never base fork work on an untagged tip.

Every behaviour the fork changes inside an official file has a section in
[fork-decisions.md](fork-decisions.md) holding its policy and the command that
proves it. The commits carry the code; that file carries the why.

## Pick the target

```bash
git fetch origin --tags
git tag --list 'v*' --sort=-v:refname | head -5
```

Take the newest tag that is an ancestor of `origin/main` and newer than your
base. Upstream releases are linear — each contains the previous — so landing
the queue on the tag in order is correct and you never need `--onto`. The
listing sorts by version, not ancestry, so confirm before basing work on it:

```bash
git merge-base --is-ancestor <tag> origin/main && echo ANCESTOR || echo NOT-ANCESTOR
```

## Rebase

```bash
git status --porcelain                                   # must be empty
git branch backup/pre-sync-$(git rev-parse --short HEAD)
git push fork backup/pre-sync-$(git rev-parse --short HEAD)
```

Push the backup. A branch that exists only on your machine is not a backup.
Delete it after the queue lands so stale backups do not pile up:

```bash
git push fork --delete backup/pre-sync-<sha>
```

Then, for each fork commit from oldest to newest, open (or reuse) a feature
branch rebased onto the new tag and land it via squash-merge once the
reported checks are green — `build` (pr-test-build job), `verify`
(fork-decisions job id), and the external `pullfrog-approval`. Never
merge upstream `main` into the fork, and never merge the fork's branches with
a merge commit — the repo rejects merge commits, so squash is the only merge
mode and the queue stays linear.

Upstream `.gitignore` covers `docs/*`, so new fork docs need `git add -f`
(the tracked files stay tracked afterwards). Remember the `-f` whenever you
add a section file under `docs/`.

Resolve each stopped commit on its own terms; `git log -1 --format=%s`
names the feature and its section in `fork-decisions.md` states the policy
you are preserving. A commit that goes empty means upstream absorbed it:
confirm their code covers the decision, `git rebase --skip`, write the id
down, and delete its section from `fork-decisions.md` **after** the rebase
finishes, as one commit. The queue shrinking is the healthy outcome.

## Verify, then push (via PR)

```bash
bash scripts/fork-verify.sh
go build -o /tmp/fork-verify-build ./cmd/server && rm -f /tmp/fork-verify-build
```

Land through a PR as usual; branch protection requires PR review checks to be
green before squash-merge. Since direct pushes and force-pushes to `main` are
rejected, the "push" step is always a squash-merge — the queue shape holds
without anyone needing push access.

## Releases from the queue

Tag the queue tip on `main` (never a feature branch), then push the tag:

```bash
git tag vA.B.C-muse.N && git push fork vA.B.C-muse.N
```

`vA.B.C` stays just above the upstream tag the queue sits on; `-muse.N`
marks fork revisions. Version bumps live in the tag — never land
package-version churn on a feature PR.

First-tag bootstrap: a fork tag with no ancestor tag makes the release job
build notes from the full repo history, which GitHub rejects (HTTP 422, body
over 125000 characters). `release-notes-cap` bounds the entries, but still
create the release with concise notes and check the `release` workflow goes
green; re-running is safe once an ancestor tag exists.

The panel (`management.html`) comes from the management-center fork's latest
release via `panel-github-repository`; release that fork first, then point the
live config at it. The proxy image is published to
`ghcr.io/<fork-owner>/cli-proxy-api` by `.github/workflows/docker-ghcr.yml`
(tag push or dispatch) — upstream's `docker-image.yml` has no DockerHub
secrets in a fork and is expected to stay red there.

## What not to do

- Merge into `main`. A merge commit means the queue is broken (squash-merge
  PRs are fine: they land as single commits).
- Treat "the symbol still exists" as preserved. After a sync, run the verify
  script — do not eyeball it.
- Take `theirs` wholesale to make a conflict go away. Re-read the decision
  section and preserve the policy, not the line numbers.
