# Kiwi shared access

The manager runs the shared-key gateway. A separate Nginx container named
`cpa-sharing-edge` binds only `127.0.0.1:18448` and allows exactly the model-list,
Chat Completions, Responses, and Messages routes under `/share/v1`.

Tailscale Funnel publishes this listener on a separate HTTPS port:

```sh
sudo tailscale funnel --bg --https=8443 http://127.0.0.1:18448
```

The public base URL is:

```text
https://kiwi.tail7b3642.ts.net:8443/share/v1
```

Set the manager's `SHARED_GATEWAY_PUBLIC_URL` to this full URL so copied key
details use the public endpoint. The admin panel remains on its private tailnet
address. Existing private Serve listeners on 443 and 9119 are independent.

The container uses `nginx@sha256:970364228b1e4f932abc22d321932816782cca8a20b85a77552a7cfe9acfb3b5`,
the adjacent `nginx.conf`, host networking, the unprivileged `nginx` user, a
read-only filesystem, a temporary `/tmp`, dropped capabilities, and
`no-new-privileges`. Its restart policy is `unless-stopped`.

## Deployment and recovery

The 2026-10-19 manager image is `cpa-manager-plus:shared-access-20261019`, built
from `cpa-manager-plus:weekly-headline-20261017` with the new embedded Linux
binary. The existing environment, tailnet bind, data volume, and routing workers
are retained, with only the public URL environment setting added.

Private rollback material is on Kiwi at
`~/.local/state/cpamp-sharing-20261019/`. It includes the previous container
metadata, a stopped-database backup `data-before.tar`, the Nginx configuration,
and the deployment manifest. Do not publish that directory.

The previous manager container is retained as
`cpa-manager-plus-before-sharing-20261019`. Stop the replacement before starting
the old manager; never run both against the same data volume. The sharing schema
is additive. Restore the stopped-database backup as well if a full data rollback
is required.

To disable only public sharing, leave the existing private Serve listeners alone:

```sh
sudo tailscale funnel --https=8443 off
docker stop cpa-sharing-edge
```

Verification must include unauthenticated rejection at `/share/v1/models`, a
successful model list with a temporary shared key, and HTTP 404 for the panel,
management endpoints, and ordinary `/v1` paths on the public listener. Remove
verification keys after testing. No provider inference is needed for these checks.
