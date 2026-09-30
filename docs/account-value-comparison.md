# Account value comparison

This personal build adds **Credential Management > Compare account value** and
background Claude quota collection to CPA Manager Plus 1.13.2.

The comparison uses the same trailing 1, 7, or 30 day interval for every account.
Choose a baseline account and compare output tokens, total tokens, or configured
API-equivalent value. Token totals include repeated cached context and are not
unique generated text. An account added during an interval has partial coverage.
Dollar values use the manager's model pricing; unpriced traffic is not free.
Optional monthly prices are prorated using 365.25 / 12 days per month. Baseline,
prices, and quota assumptions are stored in this browser, scoped to the manager URL.

The server polls Claude OAuth usage through CPA every five minutes, including
at startup. It discovers new Claude credentials automatically, uses CPA token
substitution, and stores account-wide weekly and session observations in the
existing quota tables. It does not send inference requests or change account enabled state.
Routing changes are opt-in as described below. Unsupported, unauthorized, null, and
malformed quota responses do not become zero or unlimited quota. Existing
observations remain historical evidence and the comparison labels stale data.
Model-specific or organization-wide limits are not inferred as personal capacity.

Remaining tokens are estimates, available only when the user confirms that the
quota is personal and all consumption goes through CPA. They require a fresh
observation, at least 5% consumed, and an eligible quota cycle. The calculation
uses only requests through the quota observation timestamp, avoiding newer
requests divided by an older percentage. The estimate assumes the same model
and cache mix. A new organization account still accumulates measured tokens
without exposing an allowance. No historical quota percentages are backfilled.

## Build and rollback

Run `npm ci`, then `bin/build-account-value.sh`. This builds the frontend and an
embedded Linux amd64 binary into `dist/account-value/cpa-manager-plus`, using a
temporary source copy so the generated embedded panel is not edited in Git.

Kiwi runs the binary in an image derived from `seakee/cpa-manager-plus:1.13.2`,
retaining its existing volume, bind address, and admin authentication. The old
container and a stopped-database backup are retained before replacement. Only
one manager may consume the CPA usage queue at a time. Rollback consists of
stopping the replacement container and starting the retained original container;
this change adds no schema migration, so it can reuse the existing database.
An upstream image upgrade replaces this personal feature unless it is reapplied.

Validation: Go backend suite, worker persistence and parser tests, 367 focused
account/frontend tests, frontend type check, production build, lint, and browser
verification of comparison controls. Deployment verification additionally checks
that Claude snapshots are written without opening account details.

## Automatic Claude reset priority

Set `CLAUDE_RESET_PRIORITY=true` on the manager to order enabled Claude accounts
by their next overall weekly reset. The existing five-minute quota poll supplies
fresh data. Earlier resets get higher numeric priorities; weekly-exhausted
accounts sort last. Missing, stale, or expired weekly observations prevent changes
for that poll. Disabled credentials and other providers are excluded.

Updates use the CLIProxy management fields API and change only priority metadata.
Priorities are managed automatically while enabled, so manual Claude priority
edits are overwritten at the next successful poll. CLIProxy still handles model
cooldowns and session limits; this is not proactive Fable-specific routing.
Session affinity is preserved, so existing conversations can remain on their
current account. After a weekly reset, a fresh observation reorders new sessions.
Disable the environment option and restart the manager to stop automation; the
last priorities remain in place until edited.
