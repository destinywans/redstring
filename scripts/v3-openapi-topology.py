# GET /topology has two deliberately distinct source modes. The composition
# root enables the complete V3 DTO when an operator-reviewed topology_v1
# query exists; otherwise it retains the partial CMDB constructor.
S['V3ResourceTopologyPartial'] = obj({
    'complete_v3_dto':v3_bool(), 'source':string(),
    'nodes':arr(obj({'id':string(),'name':string(),'kind':string(),'cluster':string(),
        'environment_ids':arr(string())}, ['id','name','kind','environment_ids'])),
    'edges':arr(obj({'id':string(),'from':string(),'to':string(),'kind':string(),
        'provenance':string(),'environment_ids':arr(string())},
        ['id','from','to','kind','provenance','environment_ids'])),
    'unavailable_fields':arr(string()),
    'collection':arr(obj({'environment_id':string(),'source_kind':string(enum=['kubernetes','docker','lima']),
        'state':string(enum=['fresh','partial','stale','never']),
        'sources':integer(minimum=1),'fresh_sources':integer(minimum=0),
        'last_success_at':{'type':['string','null'],'format':'date-time'},
        'oldest_success_at':{'type':['string','null'],'format':'date-time'},
        'stale_after_seconds':integer(minimum=1)},
        ['environment_id','source_kind','state','sources','fresh_sources','last_success_at','oldest_success_at','stale_after_seconds'])) | {
        'description':'Inventory collectors per environment and kind, without source names (PA-61). Resources of a collector whose last success is older than stale_after_seconds are left out, so an empty graph with stale or never is not an empty cluster. Listed only where the caller holds a resources view grant.'}},
    ['complete_v3_dto','source','nodes','edges','unavailable_fields','collection'])
S['V3TopologyGroup'] = obj({**fieldnames('id label zone'),
    **{k:integer() for k in ('x','y','w','h')}},
    ['id','label','zone','x','y','w','h'])
S['V3TopologyNode'] = obj({**fieldnames('id name brand sub group'),
    'status':string(enum=['ok','warn','danger']),
    'x':integer(),'y':integer(),'impacted':v3_bool()},
    ['id','name','sub','status','group','x','y','impacted'])
S['V3TopologyEdge'] = obj({**fieldnames('id from to label path'),
    'tone':string(enum=['ok','warn','danger']), 'fast':v3_bool(),
    'width':v3_num(),'lx':v3_num(),'ly':v3_num()},
    ['id','from','to','tone','width','path','lx','ly'])
S['V3TopologyTreeRow'] = obj({**fieldnames('kind name brand replicas'),
    'depth':integer(minimum=0),'status':string(enum=['ok','warn','danger']),
    'cpu':v3_num(),'mem':v3_num(),'open':v3_bool(),'hit':v3_bool()},
    ['depth','kind','name','status','replicas'])
S['V3TopologySankeyNode'] = obj({**fieldnames('id label color'),
    'value':v3_num()}, ['id','label','color','value'])
S['V3TopologySankeyLink'] = obj({**fieldnames('from to color'),
    'value':v3_num()}, ['from','to','color','value'])
S['V3TopologyDetail'] = obj({**fieldnames('id name meta brand owner dependents source'),
    'conns':obj({'used':integer(minimum=0),'max':integer(minimum=1)},['used','max']),
    'from_service':obj({**fieldnames('service'),
        'used':integer(minimum=0),'max':integer(minimum=1)},
        ['used','max','service']),
    'wait':arr(v3_num()),
    'health':arr(obj(fieldnames('k v'),['k','v']))},
    ['id','name','meta','brand','conns','from_service','wait','health','owner','dependents','source'])
S['V3Topology'] = obj({
    'groups':arr(ref('V3TopologyGroup')),
    'nodes':arr(ref('V3TopologyNode')),
    'edges':arr(ref('V3TopologyEdge')),
    'blast':string(),
    'tree':arr(ref('V3TopologyTreeRow')),
    'sankey':obj({'cols':arr(arr(ref('V3TopologySankeyNode'))),
        'links':arr(ref('V3TopologySankeyLink'))},['cols','links']),
    'detail':ref('V3TopologyDetail')},
    ['groups','nodes','edges','blast','tree','sankey','detail'])
v3_route('get','/topology','Read authorized source-backed topology',
    response={'oneOf':[ref('V3ResourceTopologyPartial'),ref('V3Topology')]},
    query=[v3_query('cluster'),v3_query('around'),
           v3_query('depth',integer(minimum=1,maximum=3)),
           v3_query('group_by',string(enum=['cluster','namespace','zone']))],
    desc='Without a reviewed topology_v1 catalog entry cmd/aiops returns the explicitly partial CMDB schema; with one it enables NewWithMetrics. The full Topology schema requires one selected environment, around as a visible CMDB resource ID, a single application across its at most eight authorized nodes and twelve current relations, and an operator-reviewed app/environment-scoped topology_v1 Prometheus query. Six aligned one-minute samples must cover health and impact for every node, RPS and error percentage for every edge, and the center connection, caller and wait fields, with exact resource UID/source and relation revision labels. Discovered relation sources must be fresh and in the selected environment. Layout and paths are deterministic presentation calculations; blast is an observed impacted-resource count, not a causal estimate. The frozen edge schema can only represent positive measured RPS on calls/depends_on relations; structural edges or zero-request intervals return 503. Missing, stale, conflicting or out-of-scope source evidence also fails the full request with 503 rather than zeros. The unconfigured partial constructor cannot emit the full schema. The frontend still uses fixed page data.')
P['/topology']['get']['responses']['413'] = {
    'description':'Full topology exceeds the bounded candidate or neighborhood limit',
    'content':{'application/json':{'schema':ref('Error')}}}
