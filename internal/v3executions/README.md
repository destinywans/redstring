# V3 execution detail and local report publication

This module adapts persisted `actions` records for `GET /api/v1/executions/{id}` and records local report publication for `POST /api/v1/executions/{id}/report/publish`. The source is the legacy `executions`, `action_plans`, `approvals`, `investigations`, `events`, and `investigation_reports` tables. It does not execute another action or publish to an external service.

## Evidence boundary

- `plan_id` is the real action plan ID. There is no persisted V3 proposal relationship, so `proposal_id` is `null`.
- Status and attempt come from `executions`; `status_source` identifies that source. The recovery summary comes from persisted executor result only when available. A `succeeded` status alone does not imply a report or publication.
- `steps` contains at most 100 persisted investigation events with sequence, type, and timestamp. `steps_truncated` signals additional events. UI descriptions, icons, and tones cannot be reconstructed from those rows.
- The closed report supplies its actual summary, closure actor and time, and approver. There is no trustworthy producer for chart points, thresholds, severity, impact, editorial timeline, action items, or reference prose. Those fields remain `null`; `missing` and `partial` describe the gaps.
- The frozen `web/src/v3/api/types.ts` `Execution` and mock page require populated `proposal_id`, chart and report presentation fields. WAN-117 needs a null-aware adapter and should not treat mock data as business evidence.
- Current V3 startup does not run the legacy action worker, and the legacy action create/approve/execute routes remain outside the V3 gate. These endpoints can project and locally publish eligible historical persisted executions; they do not establish a new V3 execution path by themselves.

## Authorization and publication

Both endpoints require a current selected environment and application or owner-team scoped authorization with `proposals.view`. Publication additionally requires one selected environment, a current human Cookie session, and `knowledge.manage`. Missing object grants hide the execution with 404. The publish transaction verifies the approved exact plan digest, successful execution with persisted verified recovery, and a closed report tied to the execution and corresponding investigation events. It writes a local publication ledger row and audit together. Retrying the same report digest returns the original publication; a changed report or invalid prerequisites do not overwrite it. No infrastructure operation, notification, or external postmortem publication occurs.

## Integration order

1. Run `actions.Migrate` before the module migration; its tables are migration prerequisites. Do not infer their existence from a successful Go build.
2. WAN-117 must remove or relocate the legacy `GET /api/v1/executions/{id}` registration in `internal/actions/http.go` before registering this module. Duplicate method/path registration will panic in the HTTP mux. Register the module after the legacy actions service migration and V3 authorization setup.
3. WAN-117 must load `scripts/v3-openapi-executions.py` from `scripts/v3-openapi.py` after existing definitions, then regenerate and validate the aggregate OpenAPI. The fragment is intentionally separate from the current generated contract.
4. Verify the route with a migrated isolated database and current session/grants before claiming runtime or frontend acceptance. This batch uses static checks only.
