# Kiwi quota freshness

Deployed `cpa-manager-plus:quota-refresh-20261029`, derived from the existing
`cpa-manager-plus:routing-null-fix-20261029` image. The current external panel,
volume, environment, tailnet binding, sharing edge, and restart policy are kept.

Claude's background worker still runs every five minutes. It now honors both
forms of Retry-After, skips only the affected credential, and increases repeated
failure cooldowns from 10 minutes to a maximum of one hour. Longer provider
delays take precedence. Successful observations clear the cooldown.
`/data/claude-quota-backoff.json` preserves deadlines across server restarts;
known active provider delays were seeded before deployment to avoid early retries.

Codex inspection is enabled every 10 minutes with one worker, zero immediate
retries, target provider `codex`, autoActionMode `none`, and auto recovery off.
This reads quota endpoints and stores snapshots without account mutations.
The first scheduled run returned HTTP 200 for both credentials; the older
account's weekly snapshot changed from 80% used to 19% used.

Validation: full `go test ./...`, worker `go vet`, Linux binary build, manager
health check, live cooldown persistence, and unchanged panel bytes. The local
bar still polls cached snapshots only, once per minute.

Rollback container: `cpa-manager-plus-before-quota-20261029`, kept stopped.
Deployment inputs and the private container snapshot are on Kiwi in
`~/.local/state/cpamp-quota-20261029/`. Stop/remove the current container before
renaming and starting the previous one. Never run both against the same volume.
Codex's schedule lives in manager settings and is independent of the image.
