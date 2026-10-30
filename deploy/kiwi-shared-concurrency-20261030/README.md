# Kiwi shared-key concurrency

Deployed `cpa-manager-plus:shared-concurrency-20261030`, derived from the running
`cpa-manager-plus:routing-pin-20261029` with a new Linux binary only. The panel,
volume, environment, tailnet binding, sharing edge, and restart policy are kept.

Shared keys no longer serialize requests or pause themselves. Codex and Claude Code
send overlapping requests, and a cancelled stream left the key `blocked`, so every
later request got HTTP 429, which the clients show as "Usage limit reached".
Requests now run concurrently, observed usage is still charged, and a request whose
usage cannot be confirmed is logged (`shared key ... could not be fully confirmed`)
instead of pausing the key. The allowance check still runs per request, so
concurrent requests can overshoot the final allowance a little more than before.

Validation: `go test` for the store, sharedkey, and httpapi packages; three
concurrent live requests on one key all returned 200.

Rollback container: `cpa-manager-plus-before-shared-concurrency-20261030`, kept
stopped. Inputs are on Kiwi in `~/.local/state/cpamp-shared-concurrency-20261030/`.
Stop and remove the current container, rename the previous one, and start it.
Never run both against the same volume.
