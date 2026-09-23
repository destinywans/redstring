# Delivery domain (WAN-89 / WAN-90)

PG owns versioned intent, snapshots, permissions, audit, commands and durable events.
Argo Workflows owns native DAG scheduling and execution state. GitLab owns external
pipeline execution and its approval/governance policy. Delivery does not translate
GitLab YAML or become a second task scheduler.

## Integration

Create `delivery.New(db, config)`, call `Migrate`, attach `ReleaseProvider`, register
HTTP routes with `Register(api)`, then start `Run(ctx)`. `Register` wires application
ACL and environment ownership checks. The deployment module receives `VerifySource`
and `ResolveArtifactURL` callbacks. `ReleaseProvider` is the only native deployment
path; approval, deployment and health nodes compile to engine Suspend nodes without
container commands or deployment credentials.

The native templates are executable Go examples with seven stages: immutable Git
checkout, parallel unit/vet checks, build, approval, deployment, health verification.
Source trees and check reports are Argo S3 artifacts, not shared node filesystems.
The deploy gate advances when the provider enters verification; health advances
only after its real observation window succeeds. Older definitions without a
health node keep waiting at deploy for the entire verification window.

Definitions expose `when: {from_node, output, operator: eq|ne, value}` without an
expression language. The producer must be a successful predecessor. `join: selected`
requires all selected predecessors to succeed, permits explicitly `Skipped` branches,
and requires at least one successful branch. `Omitted` or failed branches never
count as successful. Required artifacts cannot come from an optional skipped branch.

Config reads credentials from files. Native source templates currently demonstrate
public/readable Git repositories; secret injection into arbitrary task definitions
is not implemented. Do not put credentials into persisted input values or scripts.
Native tasks use an unprivileged build service account, no host mounts or deployment
secrets, CPU/memory/deadline limits and a read-only root filesystem. This is a bounded
operator build environment, not a general hostile-code sandbox or tenant boundary.

## HTTP and operations

All routes are under `/api/v1/delivery`, except authenticated GitLab webhooks under
`/api/v1/webhooks/gitlab/{source}`. Pipeline updates append immutable versions with
`expected_version`. Starting runs and queueing commands require `Idempotency-Key`.
Command kinds: `cancel`, GitLab `retry`/`play`, and read-only `reconcile`. Native retries
start a new run; they do not repeat a partially approved execution in place.

`GET /runs/{id}/stream` emits `event: delivery` with committed sequence IDs and
supports `Last-Event-ID`. `/events?after=` is its JSON equivalent. ACL is checked on
each stream iteration. `/gates` returns plan/execution links; approvals remain in the
separate deployment API. Layout and editor state do not affect execution.

GitLab actions require an explicit project binding, allowed refs, `allow_actions`
and `external_governance`. Manual jobs additionally require an allowlist. Project
listings and import preserve external identity. Trigger errors reconcile the
`AIOPS_RUN_ID` variable without replaying POST. Recovery lookup is bounded to the
latest 50 pipelines; unknown outcomes remain `needs_attention`. Retry/play writes
are not blindly replayed after a worker crash. Webhooks authenticate, deduplicate
identity, and never replace authority with their payload's claimed status. Scheduled
polling reconciles known runs; webhooks do not auto-import unknown pipelines.

Native `/runs/{id}/logs?node_id=&cursor=` uses registered S3 archives for completed
nodes, including after Pod deletion; active tasks use a bounded live tail. GitLab
logs remain governed by GitLab retention. Native run provenance still requires its
Workflow object, so Workflow TTL and automatic object deletion are disabled.

## Retention

Runs retain evidence for at least 30 days. `GET /runs/{id}/retention` exposes the
policy. Admin-only POST to the same route supports `{days:30..3650,held,reason}`;
retention cannot be shortened below the existing deadline. Verified deployment
sources and gate references protect their source run conservatively for approvals,
audit and restore. They are not released automatically.

Admin-only `POST /runs/{id}/reclaim` records a request only for an expired, terminal,
unheld, unreferenced run. The response explicitly says `cleanup_mode: manual_only`:
there is **no automatic deletion worker**. Operators must recheck references before
manual cleanup. Referenced Workflows, S3 packages and logs must remain available.

## Validation boundaries

- `go test ./internal/delivery`: deterministic unit checks; isolated PG/live checks
  skip unless explicitly configured.
- `TEST_DATABASE_URL` enables random-schema tests only on loopback review/test DBs.
- `AIOPS_DELIVERY_LIVE_LAB` is opt-in and restricted to the dedicated `aiops-test`
  delivery directory. Live tests build synthetic source and perform bounded GitLab
  actions on `codex-delivery-verification`, never production deployment.
- `AIOPS_DELIVERY_REUSE_WORKFLOW` rechecks an own retained `aiops-live-*` workflow.
- `AIOPS_DELIVERY_ARCHIVE_PROOF` supplies own completed run evidence for archived log
  retrieval after explicit removal of that workflow's Pods.

Real adapter evidence is separate from parent product HTTP/browser acceptance.
Single-process worker operation is the supported first release; this module does
not claim multi-worker HA or exactly-once external effects. Stable IDs, operation
markers, leases and reconciliation bound duplicate writes and expose uncertainty.
