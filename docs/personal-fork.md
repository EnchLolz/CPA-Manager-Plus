# Personal fork

This repository is EnchLolz's fork of [seakee/CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus).
Upstream moves fast, so the fork is kept as a short, additive patch stack that is
rebased onto each upstream release rather than merged.

## Branches and tags

| Ref | Meaning |
|---|---|
| `main` | Mirror of upstream `main`. Never commit here. |
| `personal` | Upstream release tag plus the personal patches, as a linear stack. |
| `sync/vX.Y.Z` | Automated rebase of `personal` onto upstream `vX.Y.Z`, opened as a PR. |
| `ench-vX.Y.Z-N` | Deployable build N on top of upstream `vX.Y.Z`. Triggers the image workflow. |

## What the patches add

- **Overview page** at `/` (`apps/web/src/features/overview`): per-provider remaining
  quota, per-credential windows, reset times, and the live Claude routing order.
  Dashboard moved to `/dashboard`.
- **Claude quota worker** (`apps/manager-server/internal/worker/claude_quota.go`):
  polls every Claude credential's usage every five minutes and persists the
  account-wide and model-family weekly windows as quota snapshots.
- **Reset-priority routing** (`claude_reset_priority.go`): when
  `CLAUDE_RESET_PRIORITY=true`, enabled Claude credentials get CPA `priority`
  values so the one whose weekly limit resets soonest is used first. Exhausted
  accounts drop to the bottom.
- **Codex tier routing** (`codex_tier_priority.go`): when `CODEX_TIER_PRIORITY=true`,
  enabled Codex credentials are prioritized by subscription tier, default
  `CODEX_TIER_ORDER=plus,pro,promax`, so the Plus subscription is spent first.
  The tier comes from the credential's `plan_type` or the file name suffix.
- Both workers report to `GET /v0/management/routing-priority`, which the
  Overview shows as a collapsed line under each provider heading.
- **Account value comparison** in Credential Management.

Upstream files touched, each by a line or two: `MainRoutes.tsx`, `MainLayout.tsx`,
`router.go`, `main.go`. Everything else lives in new files.

## Keeping up with upstream

`personal-sync.yml` runs daily. It mirrors upstream `main`, finds the newest stable
upstream tag, and rebases `personal` onto it:

- Clean rebase: a PR `sync/vX.Y.Z -> personal` appears. Review the upstream diff,
  merge with rebase or fast-forward, then tag.
- Conflict: an issue lists the conflicting files and the exact rebase command.

Manual equivalent:

```bash
git fetch upstream --tags
git rebase --onto vX.Y.Z "$(git describe --tags --abbrev=0 --match 'v*' personal)" personal
npm run type-check && npm run test:web && (cd apps/manager-server && go test ./...)
git push --force-with-lease origin personal
```

## Releasing and deploying

```bash
git tag ench-v1.14.2-1 personal && git push origin ench-v1.14.2-1   # builds ghcr.io/enchlolz/cpa-manager-plus
bin/personal/deploy-kiwi.sh ench-v1.14.2-1                          # pull on kiwi and recreate the container
bin/personal/deploy-kiwi.sh --local ench-v1.14.2-1                  # or build here and ship over ssh
```

The deploy script keeps the `cpa-manager-plus-data` volume and the Tailscale bind.
Roll back by deploying the previous tag.
