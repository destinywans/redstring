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

# Infrastructure overview aggregates only the caller-visible current inventory.
nullable_metric = {'type': ['number', 'null']}
nullable_time = {'type': ['string', 'null'], 'format': 'date-time'}
S['InfrastructureMetricPoint'] = obj({'time': string(format='date-time'), 'cpu': nullable_metric, 'memory': nullable_metric,
    'cpu_samples': integer(minimum=0), 'memory_samples': integer(minimum=0)}, ['time', 'cpu', 'memory'])
S['InfrastructureOverviewNode'] = obj({
    **fieldnames('id name source_id metrics_status metrics_scope'), 'kind': string(enum=['node', 'host']),
    **{k: nullable_metric for k in ['cpu_percent', 'memory_percent', 'capacity_cpu', 'capacity_memory_bytes']},
    'history': arr(ref('InfrastructureMetricPoint')),
})
S['InfrastructureOverviewCluster'] = obj({
    **fieldnames('id name source_id sync_status'), 'last_success_at': nullable_time,
    **{k: integer(minimum=0) for k in ['nodes_total', 'nodes_ready', 'nodes_unknown', 'workloads_total', 'desired_replicas', 'ready_replicas']},
})
S['InfrastructureOverviewSource'] = obj({
    **fieldnames('id name kind sync_status'), 'last_success_at': nullable_time,
    **{k: integer(minimum=0) for k in ['resources_count', 'metrics_eligible', 'metrics_current']},
})
S['InfrastructureComputeGroup'] = obj({
    **{k: integer(minimum=0) for k in ['total', 'cpu_covered', 'memory_covered', 'container_scoped']},
    **{k: nullable_metric for k in ['capacity_cpu', 'capacity_memory_bytes', 'cpu_percent', 'memory_percent']},
})
S['InfrastructureOverview'] = obj({
    'generated_at': string(format='date-time'), 'window_seconds': integer(),
    'stats': obj({k: integer(minimum=0) for k in ['resources_total', 'clusters_total', 'nodes_total', 'hosts_total', 'workloads_total', 'runtime_pods_total', 'anomalies_total', 'stale_resources_total', 'missing_resources_total']}),
    'distribution': arr(obj({**fieldnames('kind label'), 'count': integer(minimum=0)})),
    'workload_types': arr(obj({'name': string(), 'count': integer(minimum=0)})),
    'clusters': arr(ref('InfrastructureOverviewCluster')),
    'nodes': arr(ref('InfrastructureOverviewNode')),
    'nodes_truncated': {'type': 'boolean'},
    'compute_groups': obj({'node': ref('InfrastructureComputeGroup'), 'host': ref('InfrastructureComputeGroup')}),
    'sources': arr(ref('InfrastructureOverviewSource')),
    'anomalies': arr(obj(fieldnames('id name kind reason status'))),
    'anomalies_truncated': {'type': 'boolean'},
    'metrics': obj({
        **{k: integer(minimum=0) for k in ['eligible', 'current', 'partial', 'unavailable', 'unsupported', 'stale']},
        'history': arr(ref('InfrastructureMetricPoint')), 'cpu_percent': nullable_metric, 'memory_percent': nullable_metric,
    }),
}, ['generated_at', 'window_seconds', 'stats', 'distribution', 'workload_types', 'clusters', 'nodes', 'sources', 'anomalies', 'metrics'])
route('get', '/resources/overview', 'Read the authorized infrastructure dashboard', response=ref('InfrastructureOverview'),
      query=[q for q in resource_query if q['name'] not in ('page', 'page_size', 'sort')],
      desc='Aggregates caller-visible resources under existing RBAC and global environment scope. No global administrator bypass. Runtime Pods and events are separate from inventory totals; missing objects do not count as current resources. Source synchronization and metric freshness are independent of resource readiness. Node and explicitly mapped host metrics are deduplicated; 15-minute time buckets contain null for unavailable observations, never synthetic zero. No credentials or source endpoints are returned.')
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
