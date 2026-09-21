"""Accurate totals and bounded cursor or direct page reads (WAN-96)."""
for resource in ('documents', 'contexts'):
    operation = P['/memory/' + resource]['get']
    operation['responses']['200']['content']['application/json']['schema']['properties']['total'] = integer(minimum=0)
    operation['parameters'].append({'name': 'limit', 'in': 'query', 'schema': integer(minimum=1, maximum=50, default=50)})
    operation['parameters'].append({'name': 'page', 'in': 'query', 'description': 'Optional direct page read; cannot be combined with a nonempty cursor. Omit to retain cursor mode.', 'schema': integer(minimum=1, maximum=1000000)})
    operation['description'] = operation['description'].replace('50 items per page.', 'Page size is configurable from 1 to 50 (default 50). Total and candidates share the same permission/filter predicates and a read-only snapshot within each response. Document content is reauthorized before projection; concurrent revocation can omit a candidate. A cursor is authoritative for whether more candidates remain.')
    operation['description'] += ' Optional page reads the requested offset directly without visiting preceding pages, using the same filters, ACL, count and snapshot. Out-of-range pages return empty items with the actual total. Results may shift between requests when records are inserted or removed.'
