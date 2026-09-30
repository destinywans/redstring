# V3 silence execution handoff (WAN-118 → WAN-117)

The product `POST /silences` and `DELETE /silences/{id}` submit pending,
immutable proposals. They do not call Alertmanager. The backend now wires
`v3alertmanager.SilenceExecutor`, the reviewed action source, a durable
two-person Passkey approval authority and an independent worker behind
`AIOPS_V3_SILENCE_EXECUTION_ENABLED=true`. The default is off. Missing
configuration returns `501 not_connected`; a configured but unavailable
worker, approval store or source fails closed. This is conditional backend
code, not evidence that migrations or real upstream dispatch have succeeded.

## Approval and dispatch contract

1. `cmd/aiops` wires `v3proposals.SilenceExecutionActionSource{Proposals: service}` as
   `SilenceExecutor.Actions`. It reconstructs the canonical create/cancel
   action, review content, digests, and cancellation evidence in the caller's
   transaction. It does not grant approval.
2. `SilenceApprovalAuthority` provides durable, transaction-aware
   `Ready` and `VerifyApprovedWithTx`. The verifier must lock persisted
   proposal, decision, approver, and consumed passkey evidence; require the
   exact proposal ID, kind, action digest, review digest, creator, environment,
   and application; require two distinct currently active human approvers
   other than the creator, each with a fresh passkey proof and scoped approval
   grant. Return one immutable `ApprovalDigest` identifying that approval
   generation. A caller-supplied boolean or HTTP request body is not proof.
3. The approval/revocation transaction serializes against the executor's
   claim transaction. Once a claim commits, revocation cannot erase its
   history or authorize another attempt. The persisted proposal status and
   approval policy include `approved` behind the proposal-bound Passkey flow.
4. The independent worker is configured with `Ledger`, the trusted environment
   bound `Client`, `Actions`, `Approvals`, and `Worker`. The latter implements
   `SilenceWorkerReadiness` and must report a running dispatcher, not merely
   a constructed object. `Actions.Ready` must also pass. The product routes use
   `CreateReady`/`CancelReady` as their readiness gate. After approval, the worker calls
   `ExecuteApprovedCreate` or `ExecuteApprovedCancel` with the proposal ID.
   The worker must not call low-level `ReserveCreate`, `beginCreate`,
   `Client.CreateSilence`, or `Client.ExpireSilence` directly.
5. A committed `v3_silence_execution_claims` row is a one-shot dispatch
   barrier. `dispatching` or `unknown` may mean the upstream accepted the
   request. Never repeat the write. Only `ReconcileCreate` and
   `ReconcileCancel` may inspect the upstream result, and these methods make
   no Alertmanager write. `manual_review` is terminal for automation.

The approval endpoint's `200 approved` records a durable historical decision
and `execution_id` handoff. An exact challenge/body replay can return that
same response after approval evidence expires or is revoked; it does not
attest that execution is currently permitted, claimed or complete. The
executor rechecks current approval evidence when it claims a decision and
must refuse stale or revoked proof. Worker `Ready` means the loop is polling,
not that a particular decision will be processed within the 15-minute
approval lifetime; queue backlog can expire an unclaimed decision.

The executor rechecks the creator, application/environment binding, current
grants, open investigation, and immutable action in the claim transaction.
Create reserves a fixed payload and dispatches once after commit. Cancel
requires the exact confirmed create operation and a fresh full upstream
snapshot equal to the proposal evidence before dispatch. A success response
alone is not confirmation: readback must prove the result. Missing, duplicate,
changed, or uncertain upstream evidence remains `unknown` or
`manual_review` for an operator.

## Bulk alert-group silence

`POST /alert-groups/bulk` with `action=silence` also needs
`AIOPS_V3_BULK_GROUP_SILENCE_CONFIG`. An empty path leaves this action
disconnected; the file is read only when the main silence execution switch is
enabled. It must be an absolute regular 0600 JSON file with 1–500 `bindings`
entries. Each entry names one `environment_id`, authenticated ingress
`source_id`, and persisted `application_id`, plus `label_name`,
`config_revision`, `attestation_digest`, `endpoint_binding_fingerprint`,
`attestation`, and RFC 3339 `expires_at`. The attestation value is exactly
`upstream-equality-unique-stable-v1`; both digests are 64 lowercase hex
characters, and expiry must exceed the next hour. The dedicated label is
supplied by a reviewed upstream producer and forwarded unchanged to the
authenticated webhook and the bound Alertmanager endpoint. A source ID or
envelope fingerprint is never treated as an Alertmanager matcher label.

Startup validates each binding against the current instance, Alertmanager
HTTPS write source and endpoint fingerprint, policy source, unique application
label, and authenticated ingress mapping. It also requires a configured
historical alert-range coverage callback and a database pool with at least two
connections. Each request separately requires that the callback prove complete
coverage from its oldest active occurrence; an empty or incomplete coverage
binding yields `not_connected` for that group. The bulk handler requires the
same dedicated label value on all locked group members. After releasing the
group transaction, it checks the same independent
worker and approval-store readiness as ordinary silence creation before
storing an immutable proposal and group evidence. Passkey options
and finish require committed evidence before proceeding. The independent
executor prelocks the group before proposal and grant rows, and revalidates
its current members, source, matcher scope, evidence and creator authorization
inside the one-shot claim transaction. Missing or changed evidence denies
dispatch; a pending proposal alone is never permission to write upstream.

The code can check persisted current groups and bound configuration. It cannot
prove that future upstream alerts will keep the label unique, so enabling this
path requires an operator-reviewed producer/forwarder contract and a fresh
attestation. A private file by itself is not deployment or product acceptance.

The `bulk-` idempotency-key prefix is reserved for internal group proposals.
Ordinary silence create/cancel HTTP requests and the ordinary create producer
reject that prefix, so they cannot create an unapprovable proposal that the
approval and execution paths mistake for a group action. Existing records with
this prefix and no companion evidence remain blocked; deployment review must
inspect them rather than silently delete or approve them.

## Deployment and verification state

Migration 140 records immutable cancellation intent; 141 adds one-shot
execution claims; 145 stores Passkey votes and the approved decision; 146
stores bulk group evidence bound to its reviewed proposal; 148 adds approval
and uniqueness guards; 150 moves uniqueness to an active reservation that can
be released only for a rejected or truly expired proposal with no approval,
execution claim or possible upstream send. Historical evidence remains.
If 146 ran earlier, inspect duplicate group or matcher
evidence before applying 148 and resolve it explicitly; the migration will
fail rather than delete historical proposals. These migrations have not been
exercised against an existing business database in
this batch. The source and process wiring are present, but isolated database,
real Passkey, Alertmanager dispatch/readback, restart recovery and browser
validation remain open. `implemented` in the main route contract refers to
the conditional backend path, not deployed or user-accepted silence execution.
