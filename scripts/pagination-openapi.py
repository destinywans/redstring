"""Accurate list totals and bounded cursor page sizes (WAN-96)."""
for resource in ('documents', 'contexts'):
    operation = P['/memory/' + resource]['get']
    operation['responses']['200']['content']['application/json']['schema']['properties']['total'] = integer(minimum=0)
    operation['parameters'].append({'name': 'limit', 'in': 'query', 'schema': integer(minimum=1, maximum=50, default=50)})
    operation['description'] = operation['description'].replace('50 items per page.', 'Page size is configurable from 1 to 50 (default 50). Total and candidates share the same permission/filter predicates and a read-only snapshot within each response. Document content is reauthorized before projection; concurrent revocation can omit a candidate. A cursor is authoritative for whether more candidates remain.')
