# Scoped audit uses the existing management_audit source. The hash chain is
# sealed from migration time; pre-migration history is not independently proven.
S['V3AuditEntry'] = obj({**fieldnames('id time action target source'),
    'actor':obj({'kind':string(enum=['user','agent','system']),'name':string()},['kind','name']),
    # pending: a lifecycle step whose final outcome is a later row. null: an
    # outcome this server cannot classify; unavailable.result says so (PA-42).
    # unconfirmed: outcome unknown, the write is only read back or checked by
    # a person; expired: lapsed before anything ran (PA-206).
    'result':{'type':['string','null'],'enum':['ok','pending','blocked','failed','unconfirmed','expired',None]},
    'unavailable':ref('V3Unavailable')},
    ['id','time','actor','action','target','result','source'])
S['V3AuditView'] = obj({'items':arr(ref('V3AuditEntry')),
    'counts':v3_map(integer()),'volume':arr(arr(integer())),'next_cursor':string()},
    ['items','counts','volume'])
S['V3AuditDetail'] = obj({**fieldnames('id title time actor_kind actor_name payload'),
    'actor':arr(v3_object('k v')),'links':arr(v3_object('k v to')),
    'chain_ok':v3_bool()},
    ['id','title','time','actor_kind','actor_name','actor','links','payload','chain_ok'])
v3_route('get','/audit','Read environment-scoped audit events',
    response=ref('V3AuditView'),
    query=[v3_query(x) for x in ['filter','q','cursor']],
    desc='Unknown or ambiguous historical environment rows are omitted; counts reflect visible records only. An application row is listed only in the environment named by its changes.environment_id; executor, canvas reply and other rows written before PA-93 without one stay in the hash chain unchanged and are not listed.')
v3_route('get','/audit/{id}','Verify audit record chain and read redacted detail',
    response=ref('V3AuditDetail'),
    desc='chain_ok validates the sealed prefix from migration time, not independent proof of pre-migration history.')
v3_route('post','/audit/export','Create a scoped CSV export',
    body=obj({**fieldnames('filter q'),'range':string(enum=['15m','30m','1h','6h','24h','7d','30d'])}),response=obj({'url':string()},['url']),
    desc='Export is limited to 10,000 visible records and retained for one hour. range bounds the export like GET /audit range, so it holds what the list shows (PA-170); without it every time is exported.')
v3_route('get','/audit/exports/{id}','Download owner and environment-bound audit CSV',
    desc='Authenticated download checks the original environment set and export expiry.')
P['/audit/exports/{id}']['get']['responses']['200']['content'] = {
    'text/csv': {'schema':{'type':'string','format':'binary'}}}
