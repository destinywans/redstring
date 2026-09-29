# V3 postmortem dashboard read

`GET /api/v1/postmortems` reads saved `v3_postmortems` records joined to their
registered application, environment, incident, and original closing event.
It applies the request's environment scope and current `events:view` and
`knowledge:view` grants to each object. Unpublished reports are visible only
to their author or a current `knowledge:manage` holder. Grant failures omit
the object; an unavailable authorization provider fails the request.

The response retains the frozen dashboard keys: `kpis`, `items`, `causes`,
`impact_by_service`, and `followups`. An item uses the saved incident title and
severity, exact close timestamp, duration from incident start to its saved
closing event, and the saved action count. A recorded `root` section is exposed
as `root_cause` when present. The frozen `cause` field means a category, which
the source does not store, so it says `Unknown`. `workflow_status` preserves the stored `review` state; the frontend's
two-state `status` field maps review to unpublished `draft`. Because no action
completion producer exists, `actions` is `unknown / N` when N is positive.

The KPIs currently returned are `postmortem_count`, the number of visible
saved reports, and (when the count is positive) `reported_mttr`, the mean
start-to-close duration of incidents with a visible saved report. This is not
the mean of every incident in the environment. The absent MTTA, recurrence, action completion, root-cause
category, service impact, and outstanding-action evidence is listed in
`unknown_fields`; `complete_v3_dto` is `false`. Their collection fields are
empty because no records can populate them truthfully. Clients must not
interpret those empty arrays as measured zero. The source model needs typed
cause categories, impact intervals, action completion events, and incident
acknowledgement evidence before the dashboard contract can be called complete.

`tab=all|draft|review|published`, `q` (title substring, maximum 200 Unicode
characters), `page` (1–1000), and `page_size` (1–1000) filter and paginate
the authorized inventory. The default page size is 1000 for compatibility with
the current unpaged frontend. More than 1000 visible reports returns 413 rather
than silently truncating KPI or pagination totals. Malformed query values
return 422; missing storage or authorization returns 503.
