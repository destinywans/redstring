# Generic, signed V3 event ingress. Alertmanager's legacy webhook has a
# different provider payload and remains a separate public adapter.
_push_types = ['alert','change','incident','resource','oncall']
_push_tones = ['neutral','accent','ok','warn','danger','info']
S['V3PushEventType'] = obj({
    'id':string(enum=_push_types),'icon':string(),'name':string(),
    'schema':string(),'today':string(),'rejected':string(),
    'tone':string(enum=_push_tones)},
    ['id','icon','name','schema','today','rejected','tone'])
S['V3PushEndpointHeader'] = obj(fieldnames('k v note'),['k','v','note'])
S['V3PushEndpointResponse'] = obj({
    'code':string(),'tone':string(enum=_push_tones),'text':string()},
    ['code','tone','text'])
S['V3PushEndpoint'] = obj({
    'method':string(enum=['POST']),'path':string(),'host':string(),
    'headers':arr(ref('V3PushEndpointHeader')),'example':string(),
    'responses':arr(ref('V3PushEndpointResponse'))},
    ['method','path','host','headers','example','responses'])
S['V3PushDelivery'] = obj({
    **fieldnames('time source key code result ms'),
    'tone':string(enum=_push_tones)},
    ['time','source','key','code','tone','result','ms'])
S['V3PushStack'] = obj({
    'enabled':v3_bool(),
    'components':arr(obj(fieldnames('brand name role'),['brand','name','role'])),
    **fieldnames('helm compose estimate')},
    ['enabled','components','helm','compose','estimate'])
S['V3PushView'] = obj({
    'types':arr(ref('V3PushEventType')),
    'endpoint':ref('V3PushEndpoint'),
    'deliveries':arr(ref('V3PushDelivery')),
    'stack':ref('V3PushStack'),
    'availability':string(enum=['configured_unverified','endpoint_host_unconfigured','not_configured'])},
    ['types','endpoint','deliveries','stack','availability'])
v3_route('get','/ingest/push','Read authorized Push ingress configuration and receipts',
    response=ref('V3PushView'),
    desc='Requires connectors.view in the selected environment and current service visibility for each returned source. Type counts and recent receipt rows come only from persisted, authorized events; hidden sources do not enter totals. A source-free view reports zero/empty state. The optional starter stack has no deployment controller: enabled is false and install commands are empty. A configured public host is the only trusted host source; credentials and signing keys never appear in this response.')
S['V3PushTestRequest'] = obj({'type':string(enum=_push_types)},['type'])
S['V3PushTestResult'] = obj({'code':integer(enum=[202]),
    'receipt_id':integer(minimum=1)},['code','receipt_id'])
v3_route('post','/ingest/push/test','Send one authenticated local Push test event',
    body=ref('V3PushTestRequest'),response=ref('V3PushTestResult'),status=202,
    desc='Requires one selected environment, connectors.manage and current access to exactly one configured source for the requested type; zero or multiple sources return explicit errors. The server creates a bounded synthetic event and uses the same normalization and persistence transaction as external ingress; this local test does not prove that an external sender has the configured token or signing key. Synthetic tests are excluded from real accepted counts. 202 is returned only after durable event, receipt and audit commit. The frozen frontend currently omits the required type.')
P['/ingest/push/test']['post']['responses']['429'] = {
    'description':'Configured source minute limit exceeded',
    'headers':{'Retry-After':{'schema':string(),'description':'Seconds before retry'}},
    'content':{'application/json':{'schema':ref('Error')}}}

_push_states = {
    'alert':['firing','resolved'],
    'change':['started','succeeded','failed','rolled_back'],
    'incident':['open','acknowledged','resolved'],
    'resource':['created','updated','deleted'],
    'oncall':['started','ended','handoff'],
}
_push_event_refs = []
for _push_type in _push_types:
    _push_event_name = 'V3Push'+_push_type.title()+'Event'
    _push_event_properties = {
        'schema':string(enum=[_push_type+'.v1']),
        'event_id':string(minLength=1,maxLength=128,pattern='^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$'),
        'occurred_at':string(format='date-time'),
        'subject':string(minLength=1,maxLength=200),
        'title':string(minLength=1,maxLength=300),
        'state':string(enum=_push_states[_push_type]),
        'attributes':{'type':'object','maxProperties':16,
                      'propertyNames':string(pattern='^[A-Za-z][A-Za-z0-9_.-]{0,63}$'),
                      'additionalProperties':string(maxLength=512),
                      'description':'Credential-like attribute names are rejected; values are persisted only in the normalized event and excluded from the receipt and audit.'},
    }
    _push_event_required = ['schema','event_id','occurred_at','subject','title','state']
    if _push_type == 'alert':
        _push_event_properties['severity'] = string(enum=['critical','warning','info'])
        _push_event_required.append('severity')
    S[_push_event_name] = obj(_push_event_properties,_push_event_required)
    _push_event_refs.append(ref(_push_event_name))
S['V3PushEventEnvelope'] = {'oneOf':_push_event_refs,
    'description':'Select the schema matching the path type; non-alert events cannot carry severity. Timestamp and text lengths also have bounded UTF-8 byte limits at runtime.'}
S['V3PushReceiptResult'] = obj({'accepted':integer(minimum=1),
    'receipt_id':integer(minimum=1)},['accepted','receipt_id'])
for _push_type in _push_types:
    _push_path = '/events/'+_push_type
    route('post',_push_path,'Receive one signed generic V3 '+_push_type+' event','public',
          ref('V3Push'+_push_type.title()+'Event'),ref('V3PushReceiptResult'),202,
          desc='Public source protocol, not a browser session route. Requires a configured X-AIOps-Source with fixed application/environment binding and a Bearer token from a private file. Verify X-Timestamp within five minutes and X-Signature as lowercase sha256=HMAC-SHA256(timestamp + LF + source ID + LF + path type + LF + Idempotency-Key + LF + exact raw JSON bytes, separate source key). LF means one byte 0x0A, with no trailing LF after the body; all four prefix values are validated to exclude LF. Distinct configured sources cannot share token or HMAC key material. Idempotency-Key is scoped to the source and type for 24 hours. Body claims cannot change source, application or environment. Accepted events and sanitized receipts are committed with audit before 202; this does not imply downstream incident creation or outbound delivery. A duplicate returns 409, invalid envelope 422, invalid authentication/signature 401, and a per-source minute limit returns 429 with Retry-After. No raw body or credential is returned or logged.')
    _push_public = P[_push_path]['post']
    _push_public['security'] = [{'pushSourceToken':[]}]
    _push_public['responses']['429'] = {
        'description':'Configured source minute limit exceeded',
        'headers':{'Retry-After':{'schema':string(),'description':'Seconds before retry'}},
        'content':{'application/json':{'schema':ref('Error')}}}
    _push_public['parameters'] = [
        {'name':'X-AIOps-Source','in':'header','required':True,'schema':string(minLength=1,maxLength=64)},
        {'name':'Idempotency-Key','in':'header','required':True,'schema':string(minLength=1,maxLength=128)},
        {'name':'X-Timestamp','in':'header','required':True,'schema':string(description='Unix seconds')},
        {'name':'X-Signature','in':'header','required':True,
         'schema':string(pattern='^sha256=[0-9a-f]{64}$')},
    ]
