# API token domain handoff (WAN-122)

The V3 composition root registers human token management, loads `scripts/v3-openapi-api-tokens.py` into the aggregate contract, and dispatches only exact `GET /api/v1/connectors` with an Authorization header to `v3integrations.Service.BearerConnectorList` before the Cookie-only V3 gate. The deployed database and HTTP path still require separate verification.

## Contract

- `POST /api-tokens` accepts strict JSON `{ "name": string, "scopes": ["connectors:view"], "ttl": integer }`. `name` is trimmed, 1–80 Unicode code points and at most 200 UTF-8 bytes, without control or format characters. `ttl` is seconds and must be one of `3600`, `86400`, `604800`, `2592000`. Five other read-only scopes are reserved but rejected until they have an actual Bearer consumer. It returns 201 with `{ id, name, environment_id, scopes, created_at, expires_at, secret_shown_once }`.
- `GET /api-tokens` returns `{ items, has_more }` for the caller's tokens in the selected environment. It lists usable tokens before recent expired/revoked history and caps the response at 100; `has_more` reports older history beyond that bound. Items contain `id`, `name`, `environment_id`, `scopes`, `created_at`, `expires_at`, and nullable `revoked_at`; neither secret nor digest is exposed.
- `DELETE /api-tokens/{id}` returns 204 for the caller's selected-environment token, including repeat revocation. Missing or foreign IDs return 404.

All three management operations require exactly one selected environment and an active **human Cookie session**. Creation additionally requires current environment-scoped `connectors.manage` and a current environment-level grant for every requested read scope. Listing and revocation remain available to the human owner after those grants are removed, so a user can still find and disable an existing credential. They only access tokens owned by that human in the selected environment. The server generates a 256-bit secret; its plaintext appears in the successful create response once, while persistence keeps only a SHA-256 digest. Creation and first revocation each commit an audit record atomically; listing and repeat revocation do not add audit records. Never log or cache a secret.

## Bearer integration boundary

The package exposes `VerifyWithTx` for the exact Bearer connector read. Before a token authorizes that read, the handler verifies its digest and expiration/revocation, exact `connectors:view` scope, current owner/account and environment grant, selected environment and target scope in the same transaction, then rechecks expiry before commit. The verifier recognizes six read-only matrix cells, but issuance currently accepts only `["connectors:view"]`; the other five have no Bearer consumer and are rejected at creation. A machine token cannot create another token, approve a proposal, execute an action or invoke another high-risk write. The sole consumer is the environment-owned connector registration list; the token does not authorize connector targets, other read routes, or outbound Webhook redelivery. The request must name one configured environment and must not carry a Cookie session.

The frozen Webhooks page currently calls `POST /api-tokens` with an undefined body and only shows a toast; it cannot create a valid token or display the one-time secret until the frontend is adapted. Its mock token list is not durable token evidence.

## Validation boundary

The aggregate OpenAPI includes the three human management routes and the exact Bearer alternative on `GET /connectors`. A successful build and static contract check establish source consistency only; the database migration, actual HTTP/Bearer request, browser flow and external environment remain unverified under the current no-test boundary.
