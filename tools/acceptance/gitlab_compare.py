#!/usr/bin/env python3
"""Compare the product's GitLab changes with GitLab itself, item by item (D11 design §9.1 step 3).

  python3 tools/acceptance/gitlab_compare.py [prod|nonprod|all]

For each instance reads GET /changes?service_id=<checkout-api>&range=7d&sources=gitlab, then asks
GitLab (project 4, the Vault read token of aiops/lab/gitlab/read, never printed) for the same window
the product reports: deployments of environment aiops-lab, pipelines, and merge requests into main,
each by updated_at. Compares the ID sets and per item the fields the product shows, and derives the
links independently: a deployment's pipeline from its job, a main pipeline's merge request from the
merge commit and a branch pipeline's from the MR's source branch, a pipeline's deployments. Writes .local/acceptance/e2e/gitlab-compare-<instance>.json.
"""
import json
import sys
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402
from credentials import setting  # noqa: E402

GITLAB = 'http://127.0.0.1:28080/api/v4/projects/4'
ENV = {'prod': 'prod', 'nonprod': 'test'}


def gitlab_token():
    token, addr = setting('AIOPS_VAULT_TOKEN'), setting('AIOPS_VAULT_ADDR') or 'http://127.0.0.1:18200'
    req = urllib.request.Request(f'{addr}/v1/aiops/data/lab/gitlab/read', headers={'X-Vault-Token': token})
    with urllib.request.urlopen(req, timeout=10) as r:
        return json.load(r)['data']['data']['token']


def gitlab_list(token, path, query):
    out, page = [], 1
    while True:
        q = urllib.parse.urlencode({**query, 'per_page': 100, 'page': page})
        req = urllib.request.Request(f'{GITLAB}{path}?{q}', headers={'PRIVATE-TOKEN': token})
        with urllib.request.urlopen(req, timeout=20) as r:
            items, nxt = json.load(r), r.headers.get('X-Next-Page')
        out += items
        if not nxt:
            return out
        page = int(nxt)


def compare(instance, token):
    env = ENV[instance]
    sid = json.loads((LOCAL / 'rollback-services.json').read_text())[instance]['checkout-api']
    q = urllib.parse.urlencode({'service_id': sid, 'range': '7d', 'sources': 'gitlab'})
    status, product = call(instance, 'GET', f'/changes?{q}', env=env, role='operator')
    if status != 200:
        return {'instance': instance, 'ok': False, 'error': f'/changes {status} {product}'}
    window = {'updated_after': product['window']['start_at'], 'updated_before': product['window']['end_at'],
              'order_by': 'updated_at', 'sort': 'desc'}
    deps = gitlab_list(token, '/deployments', {**window, 'environment': 'aiops-lab'})
    pipes = gitlab_list(token, '/pipelines', window)
    mrs = gitlab_list(token, '/merge_requests', {**window, 'state': 'all', 'target_branch': 'main'})

    by_merge_sha = {m.get('merge_commit_sha') or m.get('squash_commit_sha'): m['iid'] for m in mrs if m.get('state') == 'merged'}
    by_source = {m['source_branch']: m['iid'] for m in mrs}

    def pipeline_mr(p):
        # A main pipeline belongs to the MR it merged; a branch pipeline to the MR from that branch.
        return by_merge_sha.get(p['sha']) if p['ref'] == 'main' else by_source.get(p['ref'])
    dep_pipeline = {d['iid']: ((d.get('deployable') or {}).get('pipeline') or {}).get('id') for d in deps}
    expect = {
        'deployments': {d['iid']: {'status': d['status'], 'sha': d['sha'], 'ref': d['ref'], 'pipeline_id': dep_pipeline[d['iid']],
                                   'job_id': (d.get('deployable') or {}).get('id'), 'merge_request_iid': by_merge_sha.get(d['sha'])}
                        for d in deps},
        'pipelines': {p['id']: {'status': p['status'], 'sha': p['sha'], 'ref': p['ref'], 'source': p.get('source'),
                                'merge_request_iid': pipeline_mr(p),
                                'deployment_ids': sorted(i for i, pid in dep_pipeline.items() if pid == p['id'])}
                      for p in pipes},
        'merge_requests': {m['iid']: {'state': m['state'], 'merge_commit_sha': m.get('merge_commit_sha'), 'source_branch': m['source_branch'],
                                      'author': (m.get('author') or {}).get('username'),
                                      'merged_by': ((m.get('merged_by') or m.get('merge_user')) or {}).get('username')}
                           for m in mrs},
    }
    got = {
        'deployments': {d['iid']: {'status': d['status'], 'sha': d['sha'], 'ref': d['ref'], 'pipeline_id': d.get('pipeline_id'),
                                   'job_id': (d.get('job') or {}).get('id'), 'merge_request_iid': d.get('merge_request_iid')}
                        for d in product.get('deployments') or []},
        'pipelines': {p['pipeline_id']: {'status': p['status'], 'sha': p['sha'], 'ref': p['ref'], 'source': p.get('source'),
                                         'merge_request_iid': p.get('merge_request_iid'), 'deployment_ids': sorted(p.get('deployment_ids') or [])}
                      for p in product.get('pipelines') or []},
        'merge_requests': {m['iid']: {'state': m['state'], 'merge_commit_sha': m.get('merge_commit_sha'), 'source_branch': m['source_branch'],
                                      'author': (m.get('author') or {}).get('username'), 'merged_by': (m.get('merged_by') or {}).get('username')}
                           for m in product.get('merge_requests') or []},
    }
    report = {'instance': instance, 'window': product['window'], 'coverage': product.get('coverage'), 'kinds': {}}
    ok = (product.get('coverage') or {}).get('gitlab') == 'complete'
    for kind in expect:
        e, g = expect[kind], got[kind]
        missing, extra = sorted(set(e) - set(g)), sorted(set(g) - set(e))
        diffs = {str(k): {f: [e[k][f], g[k][f]] for f in e[k] if e[k][f] != g[k][f]} for k in set(e) & set(g)}
        diffs = {k: v for k, v in diffs.items() if v}
        report['kinds'][kind] = {'gitlab': len(e), 'product': len(g), 'missing': missing, 'extra': extra, 'field_diffs': diffs}
        ok = ok and not missing and not extra and not diffs
        print(f"{'PASS' if not (missing or extra or diffs) else 'FAIL'}  {instance} {kind}: GitLab {len(e)}, product {len(g)}"
              f"{f', missing {missing}' if missing else ''}{f', extra {extra}' if extra else ''}{f', diffs {json.dumps(diffs)[:300]}' if diffs else ''}",
              flush=True)
    report['ok'] = ok
    (LOCAL / f'e2e/gitlab-compare-{instance}.json').write_text(json.dumps(report, ensure_ascii=False, indent=1))
    return report


def main():
    which = sys.argv[1] if len(sys.argv) > 1 else 'all'
    token = gitlab_token()
    results = [compare(i, token) for i in (ENV if which == 'all' else [which])]
    passed = sum(1 for r in results for k in r.get('kinds', {}).values() if not (k['missing'] or k['extra'] or k['field_diffs']))
    total = sum(len(r.get('kinds', {})) for r in results) or 1
    print(f'{passed}/{total} passed')
    sys.exit(0 if all(r['ok'] for r in results) else 1)


if __name__ == '__main__':
    main()
