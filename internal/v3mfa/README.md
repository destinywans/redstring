# V3 account TOTP foundation (WAN-124)

This package owns migration `122_v3_mfa_totp.sql` and optional self-account HTTP
routes. It does not authenticate a login, issue a step-up token, or satisfy
`assurance=passkey` for approvals. The integration layer must explicitly create
`Service`, configure V3 instance/environment middleware, and call `Register`
before serving requests. Until then, these routes are not served by
`cmd/aiops`, and the V3 `/me`, `/security`, and `/members` contracts remain
unchanged.

## Key and storage contract

Provision a **persistent, server-owned, random 32-byte raw key file**, mode
`0600`, at an absolute private path. `LoadKeyFile` rejects missing, symbolic
link, wrong-size, or group/world-readable files. Pass its bytes to `New` with
the active V3 instance ID and database pool. Do not put key bytes in an env
value, source file, or database. Do not regenerate the key on process restart:
stored TOTP seeds need the same key. A different key or missing migration
causes an explicit service error; there is no plaintext or default-key mode.
An eventual key rotation needs an explicit re-encryption workflow before
changing the key.

Only AES-256-GCM ciphertext and its random nonce are stored for each TOTP seed.
Authenticated associated data includes instance, account, and factor
generation; pending enrollment also binds the initiating session hash, tier,
and environment. Recovery codes are random 128-bit values returned only once
after confirmation. Storage holds independent salts and hashes. A consumed
code cannot be used again, including across process restarts. All factor
changes and accepted/denied code attempts write `management_audit` in the same
transaction. Audit records contain no seed or recovery code.

Existing sessions have no `issued_at` after migration and must log in again
before enrolling, confirming, or cancelling a factor. These mutations require
a login from the last 10 minutes. Every operation checks the current human
account, unexpired session, and selected environment grant inside its database
transaction. Pending seeds expire after 10 minutes. TOTP uses six digits and a
30-second period with a one-step clock tolerance, accepts each step once, and
locks verification for five minutes after five wrong attempts.

## Auxiliary routes, once registered

All paths require an authenticated V3 human and exactly one authorized
environment. Cookie-authenticated writes require an allowed `Origin` or
`Sec-Fetch-Site: same-origin`. `X-AIOps-Environment: all` cannot mutate a
factor. Request and response bodies must be treated as secrets and excluded
from access logs and telemetry.

| Method and path | Purpose |
| --- | --- |
| `GET /api/v1/me/mfa/totp` | State (`none`, `pending`, `expired`, `active`) and remaining recovery count; never returns seed or codes. |
| `POST /api/v1/me/mfa/totp/enrollment` | Empty body; returns seed and `otpauth` URI once. |
| `POST /api/v1/me/mfa/totp/confirm` | `{ "code": "123456" }`; consumes that TOTP step and returns recovery codes once. |
| `DELETE /api/v1/me/mfa/totp/enrollment` | Empty body; original session cancels pending enrollment. |
| `POST /api/v1/me/mfa/verify` | `{ "code": "..." }`; consumes one TOTP step or recovery code, returns per-call verification only. |
| `DELETE /api/v1/me/mfa/totp` | `{ "code": "..." }`; fresh login plus valid TOTP or recovery code removes the active factor. |

The caller should integrate the key through a private file path in V3 startup
configuration, then add the route and OpenAPI contract. These shared files are
owned by the integration task. Do not claim `/me` or `/security` is complete
just because this factor service exists. Enrollment is optional at this layer;
no MFA login policy is installed here.

## Targeted verification

Unit tests: `go test ./internal/v3mfa`.

PostgreSQL tests require a disposable loopback database named `wan124mfa`,
with `pgvector` and `pgcrypto` available, plus the explicit gate
`AIOPS_TEST_WAN124_MFA_PG=dedicated-temp-container` and `TEST_DATABASE_URL`.
The tests make and remove a unique schema. Never point them at a shared or
production database.
