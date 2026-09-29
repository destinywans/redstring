# The resource index has evidence-backed identities and relationships, but not
# the V3 topology layout, health, blast-radius or telemetry view-model yet.
S['V3ResourceTopologyPartial'] = obj({
    'complete_v3_dto':v3_bool(), 'source':string(),
    'nodes':arr(obj({'id':string(),'name':string(),'kind':string(),'cluster':string(),
        'environment_ids':arr(string())}, ['id','name','kind','environment_ids'])),
    'edges':arr(obj({'id':string(),'from':string(),'to':string(),'kind':string(),
        'provenance':string(),'environment_ids':arr(string())},
        ['id','from','to','kind','provenance','environment_ids'])),
    'unavailable_fields':arr(string())},
    ['complete_v3_dto','source','nodes','edges','unavailable_fields'])
v3_route('get','/topology','Read authorized resource identities and relationships',
    response=ref('V3ResourceTopologyPartial'),
    query=[v3_query(x) for x in ['cluster','around','depth','group_by']],
    desc='Partial CMDB projection only. Collector nodes require present inventory with an observed resource and successful source no older than five minutes, with matching environment. Each node is checked against resources.view in its persisted environment/application/team; a linked resource cannot borrow its application owner-team grant. Edges require both endpoints in a common authorized environment. group_by is currently unsupported (422); layout, health, blast radius, tree and Sankey are unavailable. No resources.view grant is automatically seeded from legacy RBAC.')
