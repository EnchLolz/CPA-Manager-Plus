# Personal fork

This repository is EnchLolz's fork of [seakee/CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus).
Stable upstream releases are merged into the personal branch, preserving the
custom features below. Kiwi tests and deploys the result automatically.

## Branches and tags

| Ref | Meaning |
|---|---|
| `main` | Legacy upstream mirror, no longer used by the updater. |
| `personal` | Stable upstream release plus the personal modifications. |
| `auto-update-candidate` | Dedicated local branch on Kiwi for integration and validation. |
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

Merge conflicts and failed validation invoke an isolated Codex repair agent.
It reads the three Git versions and preserves the custom behavior described in
`bin/personal/agent-instructions.md`. The updater independently checks, commits,
tests and builds each repair. A fresh, read-only agent reviews repaired changes
before publication. Rejected reviews feed another repair attempt.

There are at most three repairs in one run, each limited to 15 minutes. Exhausted
or unsuccessful repairs leave the current deployment running and retry after
six hours, or immediately when the personal/upstream revision changes. This
automates attempts, not a guarantee that every possible conflict is solvable.

The agent has a disposable tracked-file copy and read-only Git metadata. It has
no SSH keys, Docker socket, production data, or model gateway credential. Its
internal Docker network reaches a host-side broker that allows only model
inference endpoints. The broker uses an existing CLIProxyAPI key; the agent sees
only a short-lived broker token. The configured model and reasoning level come
from Kiwi's existing Codex configuration when installed. Credentials live in
`~/.local/state/cpamp-auto-update/agent-config.json`, mode 600, outside Git.

Agent edits to updater/CI controls, generated dependency locks, changelogs and
test deletions are rejected. Normal checks run outside the agent's environment.
Repair transcripts and review decisions are kept under `agent-runs/` in the
state directory. Validation and build output are in `validation.log` and
`build.log`. Interrupted agent containers/networks are cleaned up automatically.

Stable releases are tracked, not unreleased commits on upstream main. Changes
only to updater controls/documentation run the updater tests and install the
new control files without restarting an otherwise current manager.

Inspect the service and its machine-readable result:

```bash
systemctl --user status cpamp-auto-update.timer cpamp-auto-update.service
journalctl --user -u cpamp-auto-update.service -n 100
cat ~/.local/state/cpamp-auto-update/status.json
```

The service uses an installed copy of `bin/personal/auto-update.py`, updated
atomically together with the resolver files after successful validation. Its
`CPAMP_REPO` points at the separate checkout. Both units are in `bin/personal/`.
To install or refresh the agent configuration on Kiwi, run
`bash bin/personal/install-agent.sh` from that checkout while the updater is idle.

To trigger a check or disable automatic updates:

```bash
systemctl --user start cpamp-auto-update.service
systemctl --user disable --now cpamp-auto-update.timer
```

Manual rollback: stop the timer, stop/remove the current manager, rename the
chosen `cpa-manager-plus-rollback-<timestamp>` container to `cpa-manager-plus`,
and start it. Each rollback container retains its own original data volume.
Rolling back data also rolls back usage records collected after that update.
