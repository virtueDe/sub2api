---
name: sub2api-upstream-sync
description: Use when updating a customized Sub2API fork from Wei-Shaw/sub2api, checking a new upstream release, reconciling upstream migrations with local custom features, or preparing a custom release after an upstream version sync.
---

# Sub2API Upstream Sync

Update the fork through a reviewable sync branch. Preserve deployed custom
behavior and treat upstream synchronization, merging to fork `main`, and
production release as separate authorization stages.

## Source Selection

- `origin` is the customized fork; `upstream` is `Wei-Shaw/sub2api` and must
  remain push-disabled.
- For a version update, merge the newest verified official `vX.Y.Z` tag, not
  moving `upstream/main`. Use `upstream/main` only when the user explicitly asks
  for unreleased upstream development.
- Create `sync/vX.Y.Z` from current `origin/main`. Never perform the first merge
  directly on fork `main`.

## Inspect Before Merge

1. Require a clean, unambiguous worktree. Preserve unrelated files; do not
   stash, clean, stage, or delete them automatically.
2. Fetch `origin` and `upstream` branches plus tags. Require local `main` to
   fast-forward to `origin/main`; stop on divergence.
3. Verify remotes and the selected tag object/commit. List commits and changed
   files between the fork's upstream base and the selected release.
4. Inventory fork-only behavior before merging, especially check-in services,
   routes, frontend entry points, generated DI, release workflow, Compose, and
   deployment scripts.
5. Compare `backend/migrations` by filename and content. A deployed local
   migration whose filename collides with different upstream SQL is a hard
   stop: do not overwrite, renumber, or edit either migration without an
   explicit reconciliation plan and database evidence.

## Merge and Verify

1. Merge the selected official tag into `sync/vX.Y.Z` with an explicit merge
   commit so the upstream boundary remains visible.
2. Resolve ordinary code conflicts by understanding both behaviors. Do not
   prefer `ours` or `theirs` across a directory. Migration/schema conflicts,
   destructive data changes, and unclear authentication or billing behavior
   require user confirmation.
3. Regenerate derived code only with the repository's documented generator,
   then inspect the generated diff.
4. Because upstream sync is cross-module, run the relevant CI-equivalent checks
   from `.github/workflows/backend-ci.yml`, not only compilation. At minimum
   cover backend tests, frontend lint/typecheck/critical tests, and deployment
   script checks affected by the diff. Failed or unavailable required checks
   block integration unless the user explicitly accepts the named risk.
5. Confirm fork-only behavior is still reachable and tested. Review the full
   `origin/main..sync/vX.Y.Z` diff and `git diff --check`.

## Integrate and Release

1. Push the sync branch for review. Merge it to fork `main` only when explicitly
   requested and allowed by branch protection. Verify the merge commit and
   remote `main` afterward.
2. Synchronizing `main` does not authorize a production release.
3. When release is explicitly requested, use
   `v<upstream-version>-custom.<N>`, where `N` is the next unused local and
   remote suffix. Never move or force-update a tag.
4. Re-read `.github/workflows/release.yml`, verify the exact tag target, create
   an annotated tag, and push it. Report the Actions result separately from the
   Git push result.

## Automation Boundary

Safe automation can periodically fetch upstream, detect a newer official tag,
create/update a sync branch, run checks, and open a PR. Do not configure an
unattended workflow to auto-merge upstream, resolve conflicts, push a release
tag, or deploy production. Those steps can change migrations and fork-specific
behavior and require reviewed evidence plus explicit release authorization.

Stop on remote divergence, dirty ambiguous scope, missing official tag,
migration collision, unresolved semantic conflict, failed required checks, or
branch protection. Never bypass a stop with force push, skipped tests, blanket
conflict resolution, or automatic production tagging.
