---
name: sub2api-feature-release
description: Use when developing a Sub2API feature or bug fix from main, choosing a branch name, merging completed work, creating a release version tag, or triggering the repository's tag-based automatic deployment.
---

# Sub2API Feature Release

Use the repository's real Git state and scripts to move one change from a clean
`main` baseline through a feature branch and, when explicitly requested, a
version tag that triggers production deployment.

## Modes

- **Start development:** inspect the requested change and propose a branch name.
- **Finish development:** verify, commit, push the branch, and integrate it into
  `main` using the repository's current policy.
- **Release:** only when the user explicitly authorizes a release/tag, choose the
  next unused version, push `main`, then push the annotated tag.

Finishing development, passing tests, or merging to `main` does not authorize
creating or pushing a release tag.

## Branch Naming

Use lowercase ASCII and `/`:

| Change | Pattern | Example |
|---|---|---|
| Feature | `feature/<short-topic>` | `feature/checkin-reminder` |
| Bug fix | `fix/<short-topic>` | `fix/checkin-reward` |
| Build/CI/docs | `chore/<short-topic>` | `chore/release-automation` |

Derive `<short-topic>` from the business outcome in 2-4 hyphenated words.
Check local and remote branches; do not reuse an unrelated branch.

## Start Development

1. Inspect status and remotes, then fetch `origin`.
2. Preserve unrelated user changes. If the worktree is dirty, do not switch,
   stash, stage, or commit until the user chooses how those changes should be
   handled.
3. Require `main` to fast-forward to `origin/main`; do not merge or rebase a
   diverged `main` automatically.
4. Create the agreed branch from updated `main` and report its name.

## Finish Development

1. Run focused tests. Read commands from `backend/Makefile`,
   `frontend/package.json`, and `.github/workflows/backend-ci.yml`; there is no
   root `package.json` test contract.
2. Review `git status`, unstaged diff, staged diff, and `git diff --check`.
   Stage only the requested change; never use blanket staging when unrelated
   files exist.
3. Use a scoped Conventional Commit. Confirm committed files and remaining state.
4. Fetch again. Integrate `origin/main` into the feature branch using the
   selected merge policy; resolve conflicts and rerun affected tests.
5. Push the feature branch. Merge to `main` only when requested and allowed by
   branch protection; otherwise prepare the PR/commands and stop.
6. Verify the feature commit is reachable from `main` and local `main` matches
   `origin/main` after push.

## Release

The release workflow in `.github/workflows/release.yml` is triggered by `v*`.
The canonical version file is `backend/cmd/server/VERSION`.

1. Require a clean `main` synchronized with `origin/main` and successful
   post-integration tests.
2. Fetch tags. Read the canonical version and list local plus remote release
   tags. Never overwrite, delete, or force-update an existing tag.
3. Preserve the fork convention `v<upstream-version>-custom.<N>`. For another
   fork release on the same upstream base, increment `N`; after an upstream
   sync, reset it to `.1`. Obtain approval for the exact tag unless specified.
4. Update the canonical version only if the repository release workflow does
   not do so for this trigger. Recheck `.github/workflows/release.yml` rather
   than assuming historical behavior.
5. Create an annotated tag, verify its target, then push it. A successful push
   is not a successful deployment; report the Actions status when accessible.

## Stop Conditions

Stop when scope is ambiguous, `main` diverged, tests fail, integration is
rejected, the tag exists, or deployment is no longer tag-triggered. Never use
force push, tag replacement, blanket staging, or skipped tests as a workaround.
