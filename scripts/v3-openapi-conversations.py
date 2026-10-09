# Durable conversations; complete assistant parts are persisted before the
# completion event is published. Part variants follow the types.ts tagged union.
def v3_part(kind, fields, required=None):
    return obj({'type': string(enum=[kind]), **fields}, ['type'] + (required or []))
S['V3Part'] = {'oneOf': [
    v3_part('text', {'markdown': string()}, ['markdown']),
    v3_part('tool_calls', {'summary': string(), 'calls': arr(obj({**fieldnames('id connector tool args status'),
        'duration_ms': v3_num()})), 'tokens': integer(), 'duration_s': v3_num()},
        ['summary','calls','tokens','duration_s']),
    v3_part('chart', {'title': string(), 'unit': string(), 'labels': arr(string()),
        'series': arr(obj({'name': string(), 'values': arr(v3_num())})), 'threshold': v3_num(),
        'markers': arr(obj({'at': integer(), 'label': string(), 'tone': string()})),
        'source': string(), 'deep_link': string()}, ['title','unit','labels','series']),
    v3_part('table', {'title': string(), 'columns': arr(string()),
        'rows': arr(arr({'type': ['string','number']})), 'filter': string()}, ['title','columns','rows']),
    v3_part('image', {'title': string(), 'caption': string(), 'alt': string(),
        'diagram': string(enum=['call_graph'])}, ['title','caption','alt','diagram']),
    v3_part('files', {'files': arr(obj({**fieldnames('id name size preview'),
        'kind': string(enum=['md','csv','yaml','log','pdf'])}))}, ['files']),
    v3_part('diff', {**fieldnames('path from to'),'left': arr(string()),'right': arr(string())},
        ['path','from','to','left','right']),
    v3_part('topology', fieldnames('caption href'), ['caption','href']),
    v3_part('proposal', {**fieldnames('id title target detail'),
        'risk': string(enum=['low','medium','high']), 'reversible': v3_bool()},
        ['id','title','risk','reversible','target','detail']),
    v3_part('form', {'title': string(), 'fields': arr(obj({**fieldnames('id label'),
        'kind': string(enum=['radio','select','check']), 'options': arr(string()),
        'value': {'type': ['string','boolean']}}))}, ['title','fields']),
    v3_part('tasks', {'title': string(), 'items': arr(obj({'label': string(),
        'state': string(enum=['done','running','todo']), 'meta': string()}))}, ['title','items']),
    v3_part('citations', {'items': arr(obj({'n': integer(),'label': string(),
        'ref': v3_object('kind id label')}))}, ['items']),
    v3_part('data_gap', fieldnames('text href'), ['text','href']),
    v3_part('next_steps', {'items': arr(v3_object('icon label action'))}, ['items']),
    v3_part('status', fieldnames('text detail'), ['text']),
    v3_part('artifacts', {'ids': arr(string())}, ['ids']),
]}
S['V3ConversationRun'] = obj({'id': string(), 'agent': string(), 'version': string(),
    'model': string(), 'model_id': string(), 'reasoning': string(),
    'reasoning_label': string(), 'brand': string(),
    'tokens': integer(minimum=0), 'duration_s': v3_num(),
    'context_input_tokens': integer(minimum=0), 'context_budget': integer(minimum=0)},
    ['id','agent','version','model','tokens','duration_s'])
S['V3Message'] = obj({'id': string(), 'role': string(enum=['user','assistant']), 'author': string(),
    'created_at': string(format='date-time'), 'parts': arr(ref('V3Part')),
    'run': ref('V3ConversationRun'), 'attachments': arr(ref('V3Upload')),
    'streaming': v3_bool(), 'node_id': string(),
    'state': string(enum=['completed','streaming','running','failed']),
    # PA-46: a completed assistant reply cut at the model's output limit keeps
    # its answer and a status part; the user can continue from it.
    'finish_reason': string(enum=['max_output_tokens'])}, ['id','role','author','created_at','parts'])
S['V3ConversationStreamEvent'] = {'oneOf': [
    obj({'type': string(enum=['message.started']), 'message': ref('V3Message')}, ['type','message']),
    obj({'type': string(enum=['part.added']), 'message_id': string(), 'part': ref('V3Part')}, ['type','message_id','part']),
    obj({'type': string(enum=['text.delta']), 'message_id': string(), 'index': integer(minimum=0), 'delta': string()}, ['type','message_id','index','delta']),
    obj({'type': string(enum=['message.completed']), 'message_id': string(), 'run': ref('V3ConversationRun'),
        'finish_reason': string(enum=['max_output_tokens'])}, ['type','message_id']),
]}
S['V3ConversationSummary'] = v3_object('id title scope size updated_at incident_id', {
    'mode': string(enum=['chat','canvas']),
    'status': {'type':['string','null'], 'enum':['active','waiting_approval','resolved','archived',None],
        'description':'Null for a persisted historical investigation state without a V3 status mapping.'},
    'source': {'type':['string','null'], 'enum':['manual','alert_triage','schedule','im',None],
        'description':'Null for historical or derived records without trustworthy creation provenance.'},
    'participants': arr(string()), 'tokens': {'type':['integer','null'],'minimum':0,
        'description':'Null when any completed reply lacks trustworthy provider usage; the V3 frontend currently requires a number.'}})
S['V3Conversation'] = obj({'id': string(), 'title': string(), 'mode': string(enum=['chat','canvas']),
    'status': string(enum=['active','waiting_approval','resolved','archived']),
    'scope': obj({'service': string(), 'env': string(), 'inferred': v3_bool()}),
    'investigation_id': string(), 'messages': arr(ref('V3Message')),
    'artifacts': arr({'type':'object','additionalProperties':True}),
    'side': obj({'connectors': arr(v3_object('name', {'calls': integer()})),
        'context': obj({'used': {'type':['integer','null']}, 'budget': {'type':['integer','null']},
            'parts': arr(v3_object('label color', {'tokens': integer()}))}),
        'recalled': arr(v3_object('kind title', {'score': v3_num()}))})})
S['V3ConversationList'] = obj({'items': arr(ref('V3ConversationSummary')),
    'total': integer(), 'page': integer(), 'page_size': integer(),
    'stats': arr({'type':'object','additionalProperties':True}),
    'counts': obj({'mine':integer(),'team':integer(),
        'triage':integer(),'archived':integer()})})
v3_route('get','/conversations','List scoped conversations',response=ref('V3ConversationList'),
    query=[v3_query(x) for x in ['filter','q','tab','status','mode','page','page_size']],
    desc='Private chats and generic canvases plus application investigations are projected in one current-session, role, environment and object-scope authorization snapshot. Mine uses recorded ownership/creator, team uses current application-team membership, and triage requires a persisted initial-automation run linked to an investigation; an automation run proves triage work, not investigation creation origin. Filters, pagination, total and four counts use the same visible source set. Unknown provider usage, historical creation source and unmapped investigation status are null, never inferred from partial totals or incident links. The frozen frontend still assumes numeric tokens and a conversation-view entry gate, so full page acceptance remains pending.')
v3_route('post','/conversations','Create conversation',body=obj({'text':string(minLength=1),'context':{'type':['string','null']},'model':{'type':['string','null']}},['text']),
    response=obj({'id':string()},['id']),status=201,
    desc='text must contain non-whitespace and fit within 2 MiB UTF-8; context is limited to 4096 bytes and model to 256 bytes. The selected default model and creation audit are persisted. The first message is submitted separately.')
v3_route('get','/conversations/{id}','Read conversation and saved messages',response=ref('V3Conversation'),
    desc='The current session, selected instance environment, object owner and effective view grant are checked with persisted metadata and messages in one read snapshot. Private chats expose durable ordered rich parts and attachment references, including streaming or failed assistant state. Message.state disambiguates terminal failure from active streaming; a legacy failed row lacking a saved status part gets one derived from its persisted state. Generic canvas exchanges are node-scoped via message.node_id and have only their persisted text/model/state; a failed or running exchange yields its saved user prompt with that state and no invented assistant turn. Rich parts or provider usage cannot be reconstructed for canvas exchanges. A pure investigation ID has no linear conversation record and returns 404; its branch context must be read through the graph and per-node messages routes. Provider usage is not synthesized: run is absent and side.context.used/budget may be null when trustworthy usage is unavailable. The frozen frontend still assumes numeric context.')
conversation_turn_input = obj({'text':string(),'model':string(),'reasoning':string(),
    'attachments':arr(ref('V3Upload'))})
conversation_turn_input['anyOf'] = [
    {'required':['text'],'properties':{'text':string(minLength=1,pattern=r'\S')}},
    {'required':['attachments'],'properties':{'attachments':{'type':'array','minItems':1,'items':ref('V3Upload')}}},
]
v3_route('post','/conversations/{id}/messages','Start a durable assistant reply',
    body=conversation_turn_input,
    response=obj({'message':ref('V3Message'),'reply_id':string()},['message','reply_id']),status=202,
    desc='A nonblank text or at least one attachment is required; attachment-only requests may omit text. A second concurrent reply is rejected with 409. The user message, canonical Upload references, selected model/reasoning, acceptance audit and private image/PDF byte snapshots are durable in one transaction before 202. Persisted uploads are rechecked in that transaction for exact owner, environment, reference metadata, MIME and bytes. Paste and safe UTF-8 .txt/.log/.conf/.toml/.md/.csv/.json/.yaml/.yml files enter a private bounded text snapshot (16 KiB source, 32 KiB snapshot). A link is fetched before acceptance only when it is public HTTPS Markdown with at most 1000 URL bytes, no query or fragment, and port 443; SSRF-protected fetch rejects private addresses, redirects and unsupported content. The default fetcher rejects a response above its 128 KiB ingress cap as unreadable (422); fetched content exceeding the stricter 16 KiB turn source limit returns 413. The fetched body is frozen in the private text snapshot and never refetched for a model retry or later turn. Upload storage accepts broader HTTP/HTTPS URLs, so an uploaded link can still be rejected at send. Internal Grafana/Kibana/GitLab links are not parsed through connector credentials and return 422 when not publicly readable Markdown. PNG/JPEG/GIF/WebP images and PDFs are supplied as bounded media input on explicitly capable OpenAI Responses models (8 MiB aggregate raw bytes across the replayed conversation). An incapable model or unsupported type returns 422 before a turn is created, including when switching models with prior media in history. Invalid references or unsupported link content return 422; excessive accepted attachment source/snapshot, media bytes or known text budget return 413; a genuine link fetch failure returns 503 before a turn exists. All turns precheck saved history plus the new message before 202; image/PDF token cost and the full system/tool request remain provider-authoritative and can still fail after acceptance. Start and terminal writes recheck the persisted owner, environment, initiating session and current V3 conversation authorization in their transactions. A revoked terminal turn drops partial output rather than saving it. SSE reply_id pins durable event replay; failures persist a terminal status and release the conversation for a new turn. Provider usage in message.completed.run is optional and must not be inferred when unavailable. Evidence that would exceed the model input budget after tool calls is trimmed, oldest first, in the model request only (each trimmed result says so) instead of failing the turn (PA-45). A reply cut at the model output limit is completed, not failed: it keeps the streamed answer, adds a status part in the request language and carries finish_reason=max_output_tokens on message.completed and the message (PA-46).')
P['/conversations/{id}/messages']['post']['responses']['413'] = {
    'description':'Attachment source or model input budget exceeded before turn persistence',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/conversations/{id}/events','Resume ordered assistant events',
    query=[{'name':'Last-Event-ID','in':'header','schema':string(pattern='^[0-9]+$')},
        {'name':'reply_id','in':'query','schema':string(minLength=1,maxLength=128)}],
    desc='SSE data payloads follow V3ConversationStreamEvent. Last-Event-ID must be one nonnegative decimal event ID; invalid or duplicate values return 422 invalid_event_cursor. A positive cursor must belong to this currently authorized conversation and replays its original reply even after a newer turn starts; when reply_id is also supplied it must match the cursor reply. Without a positive cursor, reply_id selects that reply in this conversation, including after newer turns; otherwise the latest reply is selected. An unknown or malformed reply_id returns 422 invalid_reply. Connection and each bounded event batch recheck current conversation view; queued/running replies also recheck the persisted initiating session and conversation change before replay, while completed/failed history requires view only. Revocation between a batch check and network delivery can still expose that selected batch. Recovery rechecks the initiating session and current V3 authorization before retaining partial output. Early failure emits an empty message.started before status and terminal events. message.completed follows durable persistence; its run is optional when trustworthy provider usage is unavailable.')
P['/conversations/{id}/events']['get']['responses']['200']['content'] = {
    'text/event-stream': {'schema': string(), 'description': 'Each data line is a V3ConversationStreamEvent JSON object.'}}
P['/conversations/{id}/events']['get']['responses']['422'] = {
    'description':'Invalid cursor, reply ID, or mismatch between them',
    'content':{'application/json':{'schema':ref('Error')}}}
