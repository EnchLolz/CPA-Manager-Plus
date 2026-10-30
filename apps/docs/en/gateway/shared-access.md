# Temporary shared access

Full Mode can issue separate client keys for a friend. Each key has an expiration,
an exact model allowlist, and a total token allowance, estimated USD allowance, or
both. Shared keys work only at the Manager Server's `/share/v1` endpoint.

## Create a key

1. Open **Config Panel → Visual Editor → Authentication Configuration**.
2. Under **Shared access**, select **Create shared key**.
3. Enter a name, exact model IDs, expiration in days, and at least one allowance.
4. Copy the connection details. The secret appears only once.

Use the public Manager Server address, for example:

```text
Base URL: https://manager.example.com/share/v1
API key:  sk-share-...
```

Set `SHARED_GATEWAY_PUBLIC_URL` on Manager Server to the full public base URL,
such as `https://kiwi.example.ts.net:8443/share/v1`, so copied connection details
use that address instead of the private panel address.

Route `/share/v1/*` to Manager Server in your reverse proxy. Keep ordinary `/v1/*`
routes pointing to CPA. A shared key cannot authenticate directly to CPA and is
never added to CPA's normal API-key list. The gateway obtains an existing CPA
client key through the saved management connection and keeps it server-side.

For Claude Code and Anthropic SDKs that append `/v1/messages`, use
`https://manager.example.com/share` as the base instead.

The gateway supports `GET /models`, `POST /chat/completions`, `POST /responses`,
and `POST /messages`, including SSE streams. Use Bearer authentication or
`x-api-key`. Stateful response lookup, `previous_response_id`, stored responses,
background requests, and WebSocket connections are not supported. Requests are
limited to 16 MiB, buffered non-streaming responses to 32 MiB, and upstream
requests to 15 minutes.

## Allowance behavior

- Usage is charged when each request finishes. **The final request can exceed the
  remaining allowance.** These are request-boundary limits, not a reservation
  system or a guarantee against exceeding the exact amount.
- Requests on one key may run concurrently. A key is rejected once either
  allowance is exhausted; concurrent requests can overshoot it further.
- Token totals include input and output, with cached input counted once. Reasoning
  tokens already included in output are not counted twice.
- USD uses the configured model price book, including cache, context, and service
  tiers. Configure input and output prices for every allowed model before creating
  a spend-limited key. This is API-equivalent cost, not actual subscription billing
  or a percentage of a subscription allowance. Tool-specific fees are not counted.
- Blank or zero means no limit for that individual field. At least one positive
  allowance and an expiration within one year are required. There is no automatic
  budget reset.
- Expiration and revocation reject new requests. A request already running may
  finish. Revocation is available beside each key.

Usage and revocation persist in SQLite independently of the normal usage-history
retention. The secret is stored only as a hash. If a stream disconnects, an upstream
request fails ambiguously, or usage is missing, the gateway charges the usage it
observed and logs the request; the key stays usable.

Shared access is available in Full Mode, including when the normal usage collector
is disabled. The lightweight CPA-hosted panel cannot enforce these limits.
