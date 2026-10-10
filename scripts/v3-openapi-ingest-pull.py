# Server-registered, scoped Prometheus metric Pull. Executed by the aggregate
# generator after v3-openapi-connectors.py.
S['V3PullRequestParam'] = obj({
    'k':string(), 'v':string(), 'variable':v3_bool()},
    ['k','v','variable'])
S['V3PullRequestTemplate'] = obj({
    **fieldnames('method host path auth rate paging'),
    'params':arr(ref('V3PullRequestParam'))},
    ['method','host','path','params','auth','rate','paging'])
S['V3PullSampleHighlight'] = obj(fieldnames('text color'),['text','color'])
S['V3PullSample'] = obj({
    'status':integer(), 'meta':string(), 'body':string(),
    'captured_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: the read time inside meta, in RFC 3339; null without a successful read'},
    'highlights':arr(ref('V3PullSampleHighlight'))},
    ['status','meta','body','captured_at','highlights'])
S['V3PullField'] = obj({
    **fieldnames('field source transform color'),
    'required':v3_bool(),
    'state':string(enum=['ok','warn','none'])},
    ['field','required','source','color','state'])
S['V3PullPreviewRow'] = obj({
    **fieldnames('service name time value'),
    'labels':v3_map(string()), 'bad':v3_bool()},
    ['service','name','time','value','labels'])
S['V3PullPreview'] = obj({
    'rows':arr(ref('V3PullPreviewRow')),
    'series':arr({'type':'number'}),
    'checks':arr(ref('V3TagChip'))},
    ['rows','series','checks'])
S['V3PullDraft'] = obj({
    **fieldnames('id name contract_version'),
    'step':integer(),
    'request':ref('V3PullRequestTemplate'),
    'sample':ref('V3PullSample'),
    'contract':string(enum=['metric']),
    'fields':arr(ref('V3PullField')),
    'preview':ref('V3PullPreview')},
    ['id','name','step','request','sample','contract','contract_version','fields','preview'])
S['V3PullMappingSelection'] = obj({
    'name_label':string(),
    'label_keys':arr(string())}, ['name_label','label_keys'])
S['V3PullTestRequest'] = obj({'mapping':ref('V3PullMappingSelection')})
S['V3PullTestResult'] = obj({
    'ok':v3_bool(), 'rows':integer(minimum=0), 'dropped':integer(minimum=0),
    'captured_at':string(format='date-time')},
    ['ok','rows','dropped','captured_at'])

# PA-20 (2026-10-09): list of the server-configured Pull sources.
S['V3PullListItem'] = obj({
    **fieldnames('id name application_id environment_id connector_id query_id href'),
    'connector_name':{'type':['string','null']},
    'state':string(enum=['ready','source_unavailable'])},
    ['id','name','application_id','environment_id','connector_id','connector_name','query_id','state','href'])
v3_route('get','/ingest/pull','List registered metric Pull sources',
    response=obj({'items':v3_nullable(arr(ref('V3PullListItem'))),'empty_reason':v3_empty_reason(),
        'unavailable':ref('V3Unavailable')},['items','empty_reason','unavailable']),
    role='viewer with connectors.view and services.view',
    desc='Lists the sources of the private server Pull configuration in the selected environments (all is allowed for this read) that GET /ingest/pull/{id} would open: services.view on the application or owner team and environment connectors.view, checked with the Cookie session in one transaction. No PromQL, endpoint, credential reference or upstream call. state is ready when the bound enabled connector with query_metrics and the exact metric_resource target can serve a preview, otherwise source_unavailable (connector_name null). Without any configured source items is null with unavailable=not_configured; configured but none visible gives an empty list with empty_reason=no_authorized_target.')
v3_route('get','/ingest/pull/{id}',
    'Read one authorized registered metric Pull and a live bounded preview',
    response=ref('V3PullDraft'), role='viewer with connectors.view and services.view',
    desc='Only a server-registered Prometheus metric_resource source in one selected environment is supported; all returns 409 env_view_only. Current connector and application/team grants, exact target identity, enabled query_metrics tool, reviewed expression and precise Vault credential-use binding are required. A single bounded query_range read generates normalized, sanitized sample and preview; the body is not a raw upstream response. No credential reference, token, PromQL, or raw provider body is returned. Prometheus has no cursor pagination; at its single-page result limit, warnings or partial data, the operation fails closed. Source and actor request budgets, session/source recheck and sanitized audit also apply to this live GET. The V3 process registers this handler; production upstream connectivity remains deployment-specific.')
v3_route('post','/ingest/pull/{id}/test',
    'Test one registered metric Pull with a bounded label mapping',
    body=ref('V3PullTestRequest'), response=ref('V3PullTestResult'),
    role='viewer with connectors.view, connectors.manage and services.view',
    desc='Requires a JSON object: an empty object or a mapping selecting a preapproved metric name label and subset of allowed label keys; null mapping is invalid. The all environment returns 409 env_view_only. Application identity, time conversion and numeric samples come from the registered target and validated provider response. Host, path, PromQL, Vault reference, arbitrary JSONPath/script and pagination parameters cannot be supplied by the request. Runs one bounded query_range against the registered source under a database-backed actor/source rate budget, rechecks current session, target and grants after I/O, and atomically records sanitized observation and audit before returning observed rows and dropped conversion rows. No fixed-success response or partial upstream result is returned.')
for _pull_path, _pull_method in [('/ingest/pull/{id}','get'),('/ingest/pull/{id}/test','post')]:
    P[_pull_path][_pull_method]['responses']['429'] = {
        'description':'Source or actor Pull request budget exceeded',
        'headers':{'Retry-After':{'schema':string(),
            'description':'Seconds before retry'}},
        'content':{'application/json':{'schema':ref('Error')}}}
    P[_pull_path][_pull_method]['responses']['502'] = {
        'description':'Bounded upstream query failed, was partial or exceeded the result budget',
        'content':{'application/json':{'schema':ref('Error')}}}
