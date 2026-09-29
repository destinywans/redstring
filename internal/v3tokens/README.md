# API token domain handoff (WAN-122)

This package is an independent domain slice. WAN-117 must register its routes in the V3 composition root, connect the bearer verifier to the common authentication gate, and load `scripts/v3-openapi-api-tokens.py` into the aggregate OpenAPI builder. The standalone fragment does not claim those integrations are complete.

## Contract

- `POST /api-tokens` accepts strict JSON `{ "name": string, "scopes": string[], "ttl": integer }`. `name` is trimmed, 1–80 Unicode code points and at most 200 UTF-8 bytes, without control or format characters. `ttl` is seconds and must be one of `3600`, `86400`, `604800`, `2592000`. `scopes` contains 1–6 distinct exact codes: `services:view`, `events:view`, `connectors:view`, `resources:view`, `logs:view`, `traces:view`. It returns 201 with `{ id, name, environment_id, scopes, created_at, expires_at, secret_shown_once }`.
- `GET /api-tokens` returns `{ items, has_more }` for the caller's tokens in the selected environment. It lists usable tokens before recent expired/revoked history and caps the response at 100; `has_more` reports older history beyond that bound. Items contain `id`, `name`, `environment_id`, `scopes`, `created_at`, `expires_at`, and nullable `revoked_at`; neither secret nor digest is exposed.
- `DELETE /api-tokens/{id}` returns 204 for the caller's selected-environment token, including repeat revocation. Missing or foreign IDs return 404.

All three management operations require exactly one selected environment and an active **human Cookie session**. Creation additionally requires current environment-scoped `connectors.manage` and a current environment-level grant for every requested read scope. Listing and revocation remain available to the human owner after those grants are removed, so a user can still find and disable an existing credential. They only access tokens owned by that human in the selected environment. The server generates a 256-bit secret; its plaintext appears in the successful create response once, while persistence keeps only a SHA-256 digest. Creation and first revocation each commit an audit record atomically; listing and repeat revocation do not add audit records. Never log or cache a secret.

## Bearer integration boundary

The package exposes `VerifyWithTx` for the V3 HTTP gate. Before a token authorizes a read, the gate must verify its digest and expiration/revocation, exact requested scope, current owner/account and environment grant, selected environment and target object scope in the same authorization boundary. Only the six read scopes above are issuable. A machine token must never create another token, approve a proposal, execute an action or invoke another high-risk write. Registration alone does not enable bearer authentication; WAN-117 owns that gate and its route allowlist. The token cannot authorize outbound Webhook redelivery, which the user left disabled.

The frozen Webhooks page currently calls `POST /api-tokens` with an undefined body and only shows a toast; it cannot create a valid token or display the one-time secret until the frontend is adapted. Its mock token list is not durable token evidence.

## Validation boundary

For this slice, check Python syntax and execute the fragment in an isolated builder namespace to inspect its three route shapes. Compile the package after the implementation lands. No aggregate contract, browser flow, bearer gate, database migration execution or external invocation is established by this document or isolated static checks.
