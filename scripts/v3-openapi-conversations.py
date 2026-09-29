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
S['V3Message'] = obj({'id': string(), 'role': string(enum=['user','assistant']), 'author': string(),
    'created_at': string(format='date-time'), 'parts': arr(ref('V3Part')),
    'run': {'type':'object','additionalProperties':True}, 'attachments': arr({'type':'object','additionalProperties':True}),
    'streaming': v3_bool()}, ['id','role','author','created_at','parts'])
S['V3ConversationSummary'] = v3_object('id title scope size updated_at incident_id', {
    'mode': string(enum=['chat','canvas']), 'status': string(enum=['active','waiting_approval','resolved','archived']),
    'source': string(enum=['manual','alert_triage','schedule','im']),
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
    'counts': obj({'mine':integer(),'team':{'type':['integer','null']},
        'triage':{'type':['integer','null']},'archived':integer()})})
v3_route('get','/conversations','List scoped conversations',response=ref('V3ConversationList'),
    query=[v3_query(x) for x in ['filter','q','tab','status','mode','page','page_size']],
    desc='The mine/archived projection merges privately owned manual chats and generic canvases in one authorized snapshot with consistent search, page, total and known counts. Team and triage have no reliable unified source and return 503 rather than an empty list; their counts are null, stats is empty and unknown provider usage is null. Historical investigations lack trustworthy mine/participants attribution. The current frontend expects numeric tokens and a conversation-view entry gate also excludes canvas-only users, so this is not the complete page contract.')
v3_route('post','/conversations','Create conversation',body=obj({'text':string(minLength=1),'context':{'type':['string','null']},'model':{'type':['string','null']}},['text']),
    response=obj({'id':string()},['id']),status=201,
    desc='text must contain non-whitespace and fit within 2 MiB UTF-8; context is limited to 4096 bytes and model to 256 bytes. The selected default model and creation audit are persisted. The first message is submitted separately.')
v3_route('get','/conversations/{id}','Read conversation and saved messages',response=ref('V3Conversation'),
    desc='Saved user and assistant messages use durable turn order, including messages written in one transaction with tied timestamps. Accepted audited turns follow the serialized audit chain. Imported or unlinked historical rows use a deterministic timestamp fallback whose original commit order cannot be proven.')
v3_route('post','/conversations/{id}/messages','Start a durable assistant reply',
    body=obj({'text':string(),'model':string(),'reasoning':string(),
        'attachments':arr(ref('V3Upload'))},['text']),
    response=obj({'message':ref('V3Message'),'reply_id':string()},['message','reply_id']),status=202,
    desc='A second concurrent reply is rejected with 409. The user message is durable before 202. Persisted uploads are rechecked inside the turn transaction for exact reference metadata, owner, environment, supported text type, UTF-8 content and an aggregate 16 KiB source limit. Paste and .txt/.log/.md/.csv/.json/.yaml/.yml files enter a private bounded model-only text snapshot; public message parts keep references without uploaded bytes. Images, links, PDFs and other unsupported types return 422 before a turn is created; invalid references return 422 and excessive attachment or model context returns 413. The snapshot remains in later model context without refetching the upload; a later text-only turn with attachment history also checks the selected model budget before persistence. Start and terminal writes recheck the persisted owner, environment, initiating session and current V3 conversation authorization in their transactions. A revoked terminal turn drops partial output rather than saving it. Provider usage in message.completed.run is optional and must not be inferred when unavailable. This text-only slice does not establish support for every frontend Upload kind.')
P['/conversations/{id}/messages']['post']['responses']['413'] = {
    'description':'Attachment source or model input budget exceeded before turn persistence',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/conversations/{id}/events','Resume ordered assistant events',
    query=[{'name':'Last-Event-ID','in':'header','schema':string(pattern='^[0-9]+$')}],
    desc='SSE sequence IDs support reconnection. Last-Event-ID must be one nonnegative decimal event ID; invalid or duplicate values return 422 invalid_event_cursor. A positive cursor must belong to this currently authorized conversation and replays its original reply even after a newer turn starts; an unknown or deleted cursor returns 422. An omitted or zero cursor selects the latest reply. Connection and each event batch recheck current conversation view; queued/running replies also recheck the persisted initiating session and conversation change before replay, while completed/failed history requires view only. Revocation between a batch check and network delivery can still expose that selected batch. Recovery rechecks the initiating session and current V3 authorization before retaining partial output. Early failure emits an empty message.started before status and terminal events. message.completed follows durable persistence; its run is optional when trustworthy provider usage is unavailable.')
