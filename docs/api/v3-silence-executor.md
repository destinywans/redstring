# V3 silence execution handoff (WAN-118 → WAN-117)

The product `POST /silences` and `DELETE /silences/{id}` submit pending,
immutable proposals. They do not call Alertmanager. WAN-118 provides
`v3alertmanager.SilenceExecutor`, the reviewed action source, and migration 141.
WAN-117 owns the durable approval authority, passkey flow, worker lifecycle,
and process wiring. Until that wiring and schema are present, product routes
must return `501 not_connected`.

## Approval and dispatch contract

1. Wire `v3proposals.SilenceExecutionActionSource{Proposals: service}` as
   `SilenceExecutor.Actions`. It reconstructs the canonical create/cancel
   action, review content, digests, and cancellation evidence in the caller's
   transaction. It does not grant approval.
2. Implement `SilenceApprovalAuthority` with durable, transaction-aware
   `Ready` and `VerifyApprovedWithTx`. The verifier must lock persisted
   proposal, decision, approver, and consumed passkey evidence; require the
   exact proposal ID, kind, action digest, review digest, creator, environment,
   and application; require two distinct currently active human approvers
   other than the creator, each with a fresh passkey proof and scoped approval
   grant. Return one immutable `ApprovalDigest` identifying that approval
   generation. A caller-supplied boolean or HTTP request body is not proof.
3. The approval/revocation transaction must serialize against the executor's
   claim transaction. Once a claim commits, revocation cannot erase its
   history or authorize another attempt. `approved` must be a supported
   persisted proposal status; the current proposal status schema and V3
   approval policy still require WAN-117 changes.
4. Configure the independent worker with `Ledger`, the trusted environment
   bound `Client`, `Actions`, `Approvals`, and `Worker`. The latter implements
   `SilenceWorkerReadiness` and must report a running dispatcher, not merely
   a constructed object. `Actions.Ready` must also pass. Use
   `CreateReady`/`CancelReady` as the product route readiness gate. After approval, call
   `ExecuteApprovedCreate` or `ExecuteApprovedCancel` with the proposal ID.
   The worker must not call low-level `ReserveCreate`, `beginCreate`,
   `Client.CreateSilence`, or `Client.ExpireSilence` directly.
5. A committed `v3_silence_execution_claims` row is a one-shot dispatch
   barrier. `dispatching` or `unknown` may mean the upstream accepted the
   request. Never repeat the write. Only `ReconcileCreate` and
   `ReconcileCancel` may inspect the upstream result, and these methods make
   no Alertmanager write. `manual_review` is terminal for automation.

The executor rechecks the creator, application/environment binding, current
grants, open investigation, and immutable action in the claim transaction.
Create reserves a fixed payload and dispatches once after commit. Cancel
requires the exact confirmed create operation and a fresh full upstream
snapshot equal to the proposal evidence before dispatch. A success response
alone is not confirmation: readback must prove the result. Missing, duplicate,
changed, or uncertain upstream evidence remains `unknown` or
`manual_review` for an operator.

## Deployment and verification state

Migration 141 adds the one-shot claim table; it has not been applied to an
isolated PostgreSQL database in this batch. The affected Go packages compile,
but no integration test, process wiring, passkey approval, Alertmanager write,
or real upstream readback was performed. WAN-117 must verify the completed
workflow in an isolated environment before marking either product route
implemented or accepted.
