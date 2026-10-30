# Kiwi routing pin

Deployed `cpa-manager-plus:routing-pin-20261029`, derived from the running
`cpa-manager-plus:quota-refresh-20261029` with a new Linux binary and panel. The
volume, environment, tailnet binding, sharing edge, and restart policy are kept.

The Overview has "Use first" on every routed Claude and Codex row except the
current #1. It pins that credential first and the priority worker keeps ordering
the rest. The change is written to CPA within the request, without waiting for the
five-minute poll. Rows are listed in live routing order. A Claude pin ends at the
pinned account's weekly reset. A Codex pin lasts until it is unpinned. Pins are
stored in `/data/<provider>-routing-pin.json`.

Also fixed: the workers compared CPA priorities as `float64`, but the auth-file
client decodes with `UseNumber`, so every poll rewrote every priority. Polls now
write only changes.

Validation: full `go test ./...` (worker package with `-race`), `go vet`, web lint,
type-check, and the full Vitest suite. Live check: pinned
`claude-3c53bcdc-pineapplecompetitive` from the panel. The PUT took 200 ms and sent
two PATCHes, and CPA's auth files show it at priority 50. New sessions began using
it. Error paths returned 404, 409, and 401 without changing the pin.

Rollback container: `cpa-manager-plus-before-routing-pin-20261029`, kept stopped.
Inputs and the container snapshot are on Kiwi in
`~/.local/state/cpamp-routing-pin-20261029/`. Stop and remove the current container,
rename the previous one, and start it. Never run both against the same volume.
After a rollback the old worker ignores pin files and reorders by reset time.
