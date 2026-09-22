"""Resource workspace schema additions, executed by build-openapi.py."""
resource_json = {'type': 'object', 'additionalProperties': True}
S['ResourceApplication'] = obj(fieldnames('id key name environment_id environment_key environment_name'))
S['Resource'] = obj({
    **fieldnames('id kind name description external_id source_kind source_id owner_user_id owner_name owner_team_id team_name'),
    'applications': arr(ref('ResourceApplication')), 'attributes': resource_json,
    'tags': arr(string()), 'is_mock': {'type': 'boolean'},
    **{n: string(format='date-time') for n in ['created_at', 'updated_at', 'observed_at']},
})
S['ResourceRelation'] = obj({
    **fieldnames('id source_resource_id target_resource_id kind source_kind source_id description'),
    'provenance': string(enum=['declared', 'discovered']),
    'is_mock': {'type': 'boolean'}, 'updated_at': string(format='date-time'),
})
S['ResourceChange'] = obj({
    'id': integer(), **fieldnames('action description'),
    'created_at': string(format='date-time'), 'is_mock': {'type': 'boolean'},
})
S['ResourceOption'] = obj({**fieldnames('value label'), 'count': integer()})
S['ResourceFacets'] = obj({n: arr(ref('ResourceOption')) for n in ['kinds', 'applications', 'sources', 'owners', 'teams']})
S['ResourcePage'] = obj({
    'items': arr(ref('Resource')), 'total': integer(), 'page': integer(),
    'page_size': integer(), 'facets': ref('ResourceFacets'),
})
S['ResourceDetail'] = obj({
    'resource': ref('Resource'), 'related_resources': arr(ref('Resource')),
    'relations': arr(ref('ResourceRelation')), 'changes': arr(ref('ResourceChange')),
})
S['ResourceGraph'] = obj({
    'nodes': arr(ref('Resource')), 'edges': arr(ref('ResourceRelation')),
    'truncated': {'type': 'boolean'},
})
resource_query = [{'name': n, 'in': 'query', 'schema': string()} for n in ['q', 'environment_scope', 'application', 'application_id', 'sort']]
resource_query += [{'name': n, 'in': 'query', 'schema': arr(string()), 'style': 'form', 'explode': True} for n in ['kind', 'applications', 'source_kind', 'owner_id', 'team_id']]
resource_query += [{'name': 'page', 'in': 'query', 'schema': integer(minimum=1)}, {'name': 'page_size', 'in': 'query', 'schema': integer(minimum=1, maximum=100)}]
route('get', '/resources', 'Browse authorized registered resources', response=ref('ResourcePage'), query=resource_query,
      desc='PostgreSQL source. Unset filters are unrestricted except mock resources are excluded unless source_kind=mock is explicitly selected; repeated values are OR within a field. Facets cover the authorized environment subset. Unknown environment_scope is empty, not all. Shared application links are trimmed to caller access.')
route('get', '/resources/{id}', 'Read a resource and its visible relationships', response=ref('ResourceDetail'),
      query=[{'name': 'environment_scope', 'in': 'query', 'schema': string()}],
      desc='A resource outside caller access or global environment returns 404; both edge endpoints must be visible.')
route('get', '/resource-relations', 'Read a bounded resource relationship graph', response=ref('ResourceGraph'),
      query=[{'name': n, 'in': 'query', 'schema': string()} for n in ['root_id', 'q', 'kind', 'resource_kind', 'application_id', 'application', 'environment_scope', 'source_kind']] + [{'name': 'depth', 'in': 'query', 'schema': integer(minimum=1, maximum=3)}],
      desc='Static declared/discovered relationships, not runtime request traffic. At most 300 nodes and 600 edges, with explicit truncation. Mock records and edges are excluded by default and retain their source markers when explicitly selected with source_kind=mock.')
route('post', '/resources', 'Register resource metadata', 'admin',
      obj({**fieldnames('name kind description external_id owner_user_id owner_team_id'),
           'applications': arr(obj(fieldnames('id environment_id'), ['id', 'environment_id'])),
           'attributes': resource_json, 'tags': arr(string())}, ['name', 'kind', 'applications']),
      ref('Resource'), 201,
      desc='Administrator only. Validate each application/environment pair. Server sets source=manual and audit timestamps. Metadata registration never provisions infrastructure or fabricates observed health.')
