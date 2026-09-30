# WAN-124 passkey foundation

This package implements self-service WebAuthn registration, account-scoped
assertion, and credential deletion for a logged-in human member. It is an
independent backend slice. The V3 integration registers its seven optional
self-account routes when `AIOPS_V3_PASSKEY_ENABLED=true` and the trusted
instance RP/origin configuration passes validation. These routes are included
in the generated OpenAPI contract.

The integration must construct `New(Config{...})` using a server-managed
instance ID, RP ID, display name, tier, and exact HTTPS origin from deployment
configuration. Do not derive these values from request headers. Without a
stable trusted domain and HTTPS origin, `New` returns `ErrUnavailable`.
The package allows explicit nonproduction localhost HTTP tests, while the
main-process integration accepts only HTTPS. Registration
and deletion require a session issued within ten minutes and a selected V3
environment grant; all operations recheck human account, session, tier, and
grant in the same transaction.

For the existing HTTP backend behind a TLS-terminating proxy, passkey write
routes require both `api.AllowedOrigin` equal to the configured HTTPS origin
and `api.SecureCookies=true`. Direct TLS also works. Request-supplied forwarded
protocol headers are not accepted as proof of secure transport.

`Register` mounts only self-account routes. Its JSON creation/request options
are passed to the browser WebAuthn API; finish endpoints accept the resulting
credential JSON. Credential IDs in the account API are base64url. The package
uses `go-webauthn` for protocol parsing, origin/RP/challenge and signature
verification. Challenges live server-side for five minutes and are consumed
on the first completion attempt. Issuance is capped at ten ceremonies per
account per minute. New issuances opportunistically prune at most 100 challenge
rows older than 24 hours; exact retention in an idle deployment needs a
scheduled maintenance path during integration. Successful assertions persist counter and
backup state; clone warnings revoke the affected credential and fail.
Registration uses no attestation trust policy, so a verified credential proves
possession and user verification, not a particular hardware model.

Proposal approval uses a separate, action-backed ceremony. The proposal
service supplies a previously verified `ProposalBinding` with the exact
proposal ID, action kind, action digest, review content digest, and
environment. `BeginBoundProposalAssertion` binds these to a fresh five-minute
challenge. `VerifyProposalAssertionWithTx` must be called in the approval
transaction after the proposal row has been locked and the binding verified;
it consumes the challenge, updates the credential, and returns transaction-only
proof metadata. The caller must commit semantic denials to preserve one-use
consumption, and must check scoped approval authority before recording a vote.
The legacy `BeginProposalAssertion` and `FinishProposalAssertion` signatures
remain unavailable. Account `AssertionResult` must never be treated as
`assurance=passkey` for a proposal.

The full `webauthn.Credential` JSON is persisted because assertion validation
and future metadata review require it. It includes public key and authenticator
metadata, and may disclose device characteristics; restrict database access
and backups accordingly. No private authenticator key is stored.
