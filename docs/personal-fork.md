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
- **Manual pin** (`routing_pin.go`): "Use first" on an Overview row puts that
  credential at #1 while the worker keeps ordering the rest. It applies at once
  through `PUT /v0/management/routing-priority/pin` (`DELETE ?provider=` to unpin)
  and is stored in `$USAGE_DATA_DIR/<provider>-routing-pin.json`. A Claude pin ends
  at that account's weekly reset and is skipped while its weekly limit is used up;
  a Codex pin lasts until unpinned. Pins on disabled or removed credentials are
  dropped. CPA session affinity keeps running sessions on their current account,
  so a pin moves new sessions first.
- **Account value comparison** in Credential Management.

Upstream files touched, each by a line or two: `MainRoutes.tsx`, `MainLayout.tsx`,
`router.go`, `main.go`. Everything else lives in new files.

## Automatic updates on Kiwi

A persistent user timer, `cpamp-auto-update.timer`, checks the latest stable
GitHub release every 15 minutes. It runs while the Mac is offline. The dedicated
checkout is `~/.local/state/cpamp-auto-update/repo`; do not edit it manually.

The updater fetches `personal` and upstream tags, merges the latest stable
release, runs frontend lint/type checking/tests, repository tests and all Go
tests in the container toolchains declared by upstream, then builds the image.
It smoke-tests the panel in an isolated container, pushes the tested commit to
`personal`, and deploys on Kiwi. SSH authentication avoids the GitHub Actions
workflow-token restriction. The old Actions sync workflow has been removed.
The optional `personal-image.yml` workflow remains for manual GHCR publishing.

Deployment preserves the existing environment, Tailscale port binding and
restart policy. It stops the manager, copies its complete data volume, then
starts the new image with that copy. The original container and data remain
available for automatic rollback if the replacement fails its health checks.
Two prior deployments are retained. The updater has a lock and a persistent
recovery journal for interrupted deployments. There is a brief interruption
while data is copied and the replacement starts.

Conflicting merges, failed tests, network failures and unsupported deployment
layouts stop the update and keep the existing deployment. The timer retries;
it never discards custom changes to force an update. Arbitrary upstream
conflicts can still require a code fix. Stable releases are tracked, not
unreleased commits on upstream main.

Inspect the service and its machine-readable result:

```bash
systemctl --user status cpamp-auto-update.timer cpamp-auto-update.service
journalctl --user -u cpamp-auto-update.service -n 100
cat ~/.local/state/cpamp-auto-update/status.json
```

The service uses an installed copy of `bin/personal/auto-update.py`, updated
atomically after a successful deployment. Its `CPAMP_REPO` points at the
separate checkout. Both units are in `bin/personal/`.

To trigger a check or disable automatic updates:

```bash
systemctl --user start cpamp-auto-update.service
systemctl --user disable --now cpamp-auto-update.timer
```

Manual rollback: stop the timer, stop/remove the current manager, rename the
chosen `cpa-manager-plus-rollback-<timestamp>` container to `cpa-manager-plus`,
and start it. Each rollback container retains its own original data volume.
Rolling back data also rolls back usage records collected after that update.
