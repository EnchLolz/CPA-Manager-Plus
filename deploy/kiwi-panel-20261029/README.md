# Kiwi overview crash fix

The routing API can return `order: null` before a priority worker successfully
applies an order. `routingApi.ts` now normalizes missing/null orders to empty
arrays so the overview and its routing disclosure can render this state.

Deployed image: `cpa-manager-plus:routing-null-fix-20261029`, derived from
`cpa-manager-plus:shared-access-20261019`. The rebuilt web bundle is served through
`PANEL_PATH=/app/management.html`; the server binary is unchanged. Existing
environment, tailnet binding, volume, restart policy, and sharing edge were kept.

Build/test: `npm --workspace apps/web run build` and the overview Vitest tests.
Browser verification: the normal `/management.html#/` route renders its overview,
including five Claude and two Codex credentials, without the null-map crash.

Deployment files and the private container snapshot are on Kiwi at
`~/.local/state/cpamp-panel-20261029/`. The previous container is retained stopped
as `cpa-manager-plus-before-panel-20261029`. To roll back, stop/remove the current
container, rename the previous container to `cpa-manager-plus`, and start it.
Never run both containers against the data volume simultaneously.
