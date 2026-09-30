"""Bounded discovery acceptance; --live is an explicit parent-only opt-in.

Shares the readonly package's exact PG/role/process guards, fixed ports, lock,
source/binary attestation and one finally cleanup. SQL seeds synthetic people
and grants only. Publication, audit and CSV export come from public handlers.
"""
from __future__ import annotations

import argparse
import csv
import hashlib
import io
import json
from pathlib import Path
import re
import uuid
from urllib.parse import quote as urlquote, urlencode

import api_readonly_chain as readonly

MODULE = 'formal-discovery'
ROOT = readonly.ROOT
check, Blocked = readonly.check, readonly.Blocked


def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                          evidence_mode='real_local', **fields)), flush=True)


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.export_url = None
        self.title = self.marker + ' synthetic keyword reference'
        self.state.update(suite=MODULE, synthetic_input_scope='new users and environment grants only',
                          actual_results_source='HTTP Runbook create/submit/independent review; native audit and CSV export',
                          external_identity_verified=False)
        readonly._private(self.work / 'state.json', self.state)

    def seed(self):
        super().seed()
        statements = ['BEGIN;']
        for user in ('reader', 'peer'):
            for action in ('view', 'manage'):
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                    ','.join(readonly.quote(x) for x in (self.roles[user], 'knowledge', action, 'environment', 'test')) + ');')
        # Allow an actual selected staging read, without staging audit/member
        # privileges, so absence is not inferred from middleware rejection.
        for module in ('environment', 'knowledge'):
            statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                ','.join(readonly.quote(x) for x in (self.roles['reader'], module, 'view', 'environment', 'staging')) + ');')
        statements.append('COMMIT;')
        self.sql('\n'.join(statements))

    def knowledge(self):
        draft, _ = self.call('reader', 'POST', '/api/v1/runbooks',
            {'title': self.title, 'source': 'blank', 'status': 'draft',
             'steps': [{'kind': 'check', 'title': 'Read synthetic reference',
                        'body': self.marker + ' explicitly synthetic reference, never live service state', 'readonly': True}]},
            want=201, scenario='fixture')
        identifier = draft.get('id')
        check(isinstance(identifier, str) and re.fullmatch('[a-f0-9]{32}', identifier), 'new Runbook identifier differs')
        self.ids['runbook'] = identifier
        self.call('reader', 'POST', '/api/v1/knowledge/search', {'q': self.marker},
                  validate=lambda v: check(v['hits'] == [], 'unpublished draft leaked into keyword results'))
        self.call('reader', 'POST', '/api/v1/runbooks/' + identifier + '/submit', {}, scenario='fixture')
        self.call('reader', 'POST', '/api/v1/runbooks/' + identifier + '/reviews',
                  {'decision': 'approve', 'note': 'Self review must fail'}, want=403,
                  code='self_review_forbidden', scenario='authorization')
        self.call('peer', 'POST', '/api/v1/runbooks/' + identifier + '/reviews',
                  {'decision': 'approve', 'note': 'Independent synthetic local review; no enterprise identity proof'},
                  index=1, scenario='fixture', validate=lambda v: check(v['status'] == 'published', 'HTTP review did not publish'))
        self.call('reader', 'POST', '/api/v1/knowledge/search', {'q': self.marker, 'top_k': 5}, index=1,
                  validate=self.keyword_hit)
        for user, env in [('reader', 'staging'), ('denied', 'test')]:
            self.call(user, 'POST', '/api/v1/knowledge/search', {'q': self.marker}, environment=env,
                      scenario='authorization', validate=lambda v: check(v['hits'] == [], 'unauthorized scope leaked keyword content'))
        self.call('reader', 'POST', '/api/v1/knowledge/search', {'q': self.marker, 'top_k': 21},
                  want=422, code='invalid_query', scenario='boundary')

    def keyword_hit(self, value):
        check(value.get('mode') == 'keyword' and value.get('degraded') is True, 'local keyword fallback was not explicit')
        hits = value.get('hits')
        check(isinstance(hits, list) and hits, 'keyword result was empty')
        check(all(x.get('title') == self.title and self.marker in x.get('snippet', '')
                  and x.get('keyword', 0) > 0 and x.get('score', 0) > 0 and x.get('vector') == 0
                  for x in hits), 'keyword content/ranking differs from the exact synthetic source')

    def people(self):
        def path(name):
            return '/api/v1/people/' + urlquote(name, safe='') + '/peek'
        self.call('reader', 'GET', path(self.marker + ' peer'), index=1,
                  validate=lambda v: check(v['id'] == self.users['peer'] and v['name'] == self.marker + ' peer'
                      and v['oncall'] is None and v['load'] is None and v['role'] is None,
                      'person peek identity or unknown external facts differ'))
        self.call('reader', 'GET', path(self.marker + ' hidden'), want=404, code='not_found', scenario='authorization')
        self.call('denied', 'GET', path(self.marker + ' peer'), want=404, code='not_found', scenario='authorization')
        self.call('reader', 'GET', path('x' * 201), want=422, code='invalid_person_name', scenario='boundary')

    def audit_export(self):
        identifier = self.ids['runbook']
        rows = json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('id',id::text,'action',action,'actor',actor_id)), '[]'::json) "
            'FROM management_audit WHERE target=' + readonly.quote(identifier), read=True))
        expected = {'runbook.created': self.users['reader'], 'runbook.submitted': self.users['reader'],
                    'runbook.review.approved': self.users['peer']}
        check(len(rows) == 3 and {x['action']: x['actor'] for x in rows} == expected,
              'native HTTP publication audit differs; no synthetic audit is permitted')
        ids = {x['id'] for x in rows}
        self.call('reader', 'GET', '/api/v1/audit?' + urlencode({'q': self.marker}), index=1,
                  validate=lambda v: check({x['id'] for x in v['items']} == ids, 'native audit list differs from persisted HTTP actions'))
        first = max(ids, key=int)
        self.call('reader', 'GET', '/api/v1/audit/' + first,
                  validate=lambda v: check(v['id'] == first and v['chain_ok'] is True, 'native audit chain verification failed'))
        self.call('reader', 'POST', '/api/v1/audit/export', {'q': self.marker}, validate=self.accept_export_url)
        self.download_csv({x['id']: x for x in rows}, identifier)
        self.call('peer', 'GET', self.export_url, want=404, code='not_found', scenario='authorization')
        self.call('reader', 'GET', self.export_url, environment='staging', want=403,
                  code='forbidden', scenario='authorization')
        self.call('denied', 'POST', '/api/v1/audit/export', {}, want=403, code='forbidden', scenario='authorization')
        self.call('reader', 'POST', '/api/v1/audit/export', {'filter': 'invalid'}, want=422,
                  code='invalid_filter', scenario='boundary')
        persisted = json.loads(self.sql("SELECT json_build_object('owner',owner_id,'environments',environment_ids,"
            "'includes_global',includes_global,'csv_sha256',encode(digest(csv_data,'sha256'),'hex')) FROM v3_audit_exports WHERE id=" +
            readonly.quote(self.export_url.rsplit('/', 1)[1]), read=True))
        check(persisted == {'owner': self.users['reader'], 'environments': ['test'], 'includes_global': False,
                            'csv_sha256': self.csv_sha256}, 'native export ownership/scope/persisted CSV differs')
        self.sql('DELETE FROM v3_role_grants WHERE role_id=' + readonly.quote(self.roles['reader']) + " AND module='audit';")
        self.call('reader', 'GET', self.export_url, want=403, code='forbidden', scenario='authorization', index=1)
        readonly._private(self.work / 'native-discovery-evidence.json', {'runbook_id': identifier, 'audit_rows': rows,
            'export': persisted, 'sql_business_result_writes': 0, 'physical_reviewer_identity_verified': False})

    def accept_export_url(self, value):
        url = value.get('url')
        check(isinstance(url, str) and re.fullmatch('/api/v1/audit/exports/audit-[a-f0-9]{36}', url),
              'export URL left exact local native route')
        check(self.export_url is None, 'only one export is allowed in this bounded suite')
        self.export_url = url

    def download_csv(self, expected_rows, target):
        if (self.export_url is None or not re.fullmatch('/api/v1/audit/exports/audit-[a-f0-9]{36}', self.export_url)
                or 'reader' not in self.cookies or target != self.ids.get('runbook')):
            raise Blocked('CSV requires the exact newly issued local export and owner session')
        canonical, route_class = self.contract.canonical('GET', self.export_url)
        self.calls += 1
        row = dict(name='discovery.http.' + str(self.calls), method='GET', path=canonical, route_class=route_class,
                   scenario='normal', expected={'status': [200]}, run_id=self.run_id,
                   call_id=self.run_id + '-' + str(self.calls), test_name='FormalDiscoveryChain', registration_evidence=False)
        failure = reporting = None
        try:
            status, raw, headers = readonly.transport(readonly.BASES[1], 'GET', self.export_url,
                headers={'Origin': readonly.BASES[1], 'X-AIOps-Environment': 'test', 'Cookie': self.cookies['reader']})
            row['actual'] = {'status': status}
            check(status == 200, 'native CSV HTTP status differs')
            normalized = {k.lower(): v for k, v in headers.items()}
            check(normalized.get('content-type') == 'text/csv; charset=utf-8'
                  and normalized.get('content-disposition') == 'attachment; filename="audit.csv"'
                  and normalized.get('cache-control') == 'no-store', 'native CSV headers differ')
            reader = csv.DictReader(io.StringIO(raw.decode('utf-8')))
            check(reader.fieldnames == ['id', 'time', 'actor_id', 'actor_kind', 'action', 'target', 'outcome'], 'CSV columns differ')
            records = list(reader)
            check(len(records) == 3 and {x.get('id') for x in records} == set(expected_rows)
                  and all(x.get('target') == target and x.get('outcome') == 'succeeded'
                          and x.get('actor_kind') == 'user'
                          and x.get('actor_id') == expected_rows[x['id']]['actor']
                          and x.get('action') == expected_rows[x['id']]['action']
                          for x in records), 'CSV lacks exact native source rows')
            self.csv_sha256 = hashlib.sha256(raw).hexdigest()
            readonly.private_file(self.work / 'native-export.csv', raw.decode('utf-8'))
            row.update(status='passed', registration_evidence=True)
        except Exception as error:
            failure = error
            row.setdefault('actual', {'status': None})
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            row['status'] = 'failed'
            try:
                self.first_red(row)
            except BaseException as error:
                reporting = error
        finally:
            # Every sink is attempted, and an output/storage fault cannot
            # replace the original CSV contract failure. Shared execute still
            # owns the one finally cleanup if either kind of failure occurs.
            try:
                readonly.append_json(self.work / 'interfaces.jsonl', row)
            except BaseException as error:
                reporting = reporting or error
            try:
                fields = {'interface': row} if type(row['actual']['status']) is int else {}
                emit(row['name'], row['status'], row['actual'], **fields)
            except BaseException as error:
                reporting = reporting or error
        if failure is not None:
            raise failure
        if reporting is not None:
            raise reporting

    def run(self):
        self.provision(); self.start(); self.seed()
        for user in ('reader', 'peer', 'hidden', 'denied'):
            self.login(user)
        for step in (self.knowledge, self.people, self.audit_export):
            self.frozen(); step()
        self.frozen()
        result = dict(suite=MODULE, run_id=self.run_id, database=self.database, http_results=self.calls,
            business_operations=9, source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
            synthetic_input='local users and grants only; new HTTP-authored Runbook',
            verified=['nonempty keyword fallback', 'unpublished exclusion', 'environment/RBAC isolation',
                      'exact-name person peek', 'native audit chain', 'owner/scope/revocation restricted CSV'],
            external_todo=['enterprise physical reviewer identity', 'authoritative directory/oncall/workload', 'vector provider'])
        readonly._private(self.work / 'result.json', result)
        emit('discovery.external_sources', 'external_todo', {'unverified': result['external_todo'],
             'local_keyword_and_native_export_verified': True})
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live:
        parser.error('actual execution requires --live; parent must run sequentially after the performance suite')
    try:
        with readonly.exclusive(args.root), readonly.cooperative_signals():
            data = readonly.doctor(args.root)
            readonly.execute(Chain(args.root, data, str(uuid.uuid4())))
    except Blocked as error:
        emit('discovery.environment', 'blocked', {'error': str(error)})
        return 2
    except Exception as error:
        emit('discovery.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)})
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
