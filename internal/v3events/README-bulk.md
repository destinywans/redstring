# V3 alert-group bulk actions

`POST /api/v1/alert-groups/bulk` accepts a JSON object with `ids` (1–20 unique `group:` IDs, each followed by 24 lowercase hex characters) and `action` (`claim`, `investigate`, or `silence`). A write requires one selected environment. The group ID uses the same persisted environment row ID, application ID, source ID, and fingerprint as `GET /alert-groups`; it is a derived identity, not a client-supplied incident ID. The handler resolves and locks only the current members of that exact group, then applies current application or owner-team authorization.

The response preserves input order and has one result per requested ID: `{id,outcome,matched,changed,investigation_id,proposal_id,error}`. IDs and error are `null` when they do not apply. `matched` counts persisted members in a bounded, authorized group and is zero when the group is hidden, missing, or too large to count safely; `changed` counts incident rows changed and is zero for a pending silence proposal. `200` means every item succeeded. `207` means at least one item failed, so callers must inspect every result. A missing or hidden group returns `not_found`; routine group conflicts and disconnected sources appear in the corresponding item. An invalid batch, an aggregate environment, or unavailable shared storage or policy fails the request before item results.

| Action | Successful outcome | Durable effect and retry |
| --- | --- | --- |
| `claim` | `claimed`, `already_claimed` | Set the platform assignee on eligible persisted members; the same user's retry does not add another change. It does not change Alertmanager state or notification delivery. |
| `investigate` | `investigating`, `already_investigating` | Create or reuse a real investigation and root branch linked to the locked group snapshot. Return only a persisted investigation ID. |
| `silence` | unavailable (`not_connected`) | No proposal or upstream write. Current stored group identity includes source and fingerprint, while the available Alertmanager matcher would cover application and alertname more broadly. |

Silence is currently fail closed for every item. The stored group is separated by source and fingerprint; the available proposal matcher cannot prove it will affect only that group. A future exact matcher/source proof and separate approval/execution integration are required before enabling this action. A `not_connected` result must not be displayed as an active or pending silence.

Item failures include `not_found`, `conflict`, `not_connected`, `group_too_large`, `session_revoked`, `human_required`, and `unavailable`. Investigate also requires the current `canvas.change` grant. No direct upstream silence, infrastructure write, or external notification occurs in this handler. V3 approval and independent execution are separate, as yet unwired paths.

The standalone contract fragment is [`scripts/v3-openapi-alert-groups-bulk.py`](../../scripts/v3-openapi-alert-groups-bulk.py). The frozen frontend currently sends unsupported `merge` and `assign` actions, ignores per-item results, announces silence success immediately, and links investigation to a hardcoded ID. Its adapter must handle the three supported actions, status `207`, real returned IDs, and `pending_approval` wording before browser acceptance. This batch does not change the frontend or aggregate OpenAPI loader.

The V3 composition root registers `NewBulk` with the existing V3 policy. It deliberately leaves the silence source unset while exact fingerprint scope is unproven. Aggregate OpenAPI and the main contract include the conditional claim/investigate success path; database/runtime and frontend behavior remain unverified.
