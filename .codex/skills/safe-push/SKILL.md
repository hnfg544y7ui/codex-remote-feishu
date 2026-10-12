---
name: safe-push
description: "Use when pushing committed changes from this repository, especially when `git push` may fail because the remote branch advanced. Prefer the repo helper script that fetches, rebases if needed, reruns tests after a successful rebase, and only then pushes."
---

# safe-push

Use this skill when the task is to push already-committed local changes from this repository.

Prefer this command from the repo root:

```bash
./safe-push.sh
```

Before repeatedly checking whether the branch is publish-ready, prefer:

```bash
bash scripts/dev/worktree-facts.sh
```

Do not default to raw `git push origin <branch>` when this helper fits the task.

## Default behavior

The helper only automates the safe happy path:

1. require a clean worktree
2. trust the repo-local pre-commit guardrail record only when its tree hash exactly matches the current `HEAD`; otherwise rerun the lightweight guardrails
3. `git fetch` the target branch
4. if the remote branch moved ahead, `git rebase` onto it
5. rerun `go test ./...` only when a rebase actually happened
6. if a rebase happened, require an explicit post-rebase review before push
7. if no drift is found, continue push; if drift is found, fix it first and then continue push

## Important limits

- It does not auto-resolve rebase conflicts.
- It does not auto-handle test failures.
- It does not auto-decide whether a rebase changed the intended direction or implementation.
- The pre-commit validation record lives in this repository's git metadata and is never shared with other repositories.
- A missing, stale, or non-matching validation record never skips checks.
- On conflict or test failure, it stops and leaves the repo state visible for manual handling.
- After a successful rebase, it requires a manual audit of:
  - whether the implementation direction still matches the intended plan
  - whether the implementation still matches the intended behavior after rebasing onto the latest branch state
- If drift is found, fix it first, then continue.
- In non-interactive shells, confirm the audit with:

```bash
./safe-push.sh --confirm-rebase-review
```

## Useful variants

- Push a non-default branch:

```bash
./safe-push.sh --branch feature-x
```

- Publish a new branch for the first time:

```bash
./safe-push.sh --branch feature-x --create-branch
```

  The current branch must have no upstream. The remote query must succeed and
  prove the ref absent; the push uses an empty expected ref lease so a branch
  created concurrently cannot be overwritten. Existing remote branches retain
  the normal fetch/rebase/review path.

- Use a narrower post-rebase test command:

```bash
./safe-push.sh --test-cmd 'go test ./internal/adapter/feishu ./internal/core/orchestrator ./internal/app/daemon'
```

- Force tests even when no rebase happened:

```bash
./safe-push.sh --always-test
```

- Skip tests entirely:

```bash
./safe-push.sh --no-test
```
