import contextlib
import copy
import hashlib
import io
import json
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_runbook_audit_rollback_coverage as rb


class RunbookAuditTests(unittest.TestCase):
    def fixture(self):
        actor, uid, source = 'a'*32, 'b'*32, 'c'*32
        body = b'# inert Runbook source\nSynthetic only\n'
        draft = dict(source_sha256=hashlib.sha256(body).hexdigest(), sources=['import:'+source],
            draft_mode='source_outline', steps=[dict(n=1,kind='check',title='Review imported Markdown',
            body=body.decode(),readonly=True)], missing=['service_scope','trigger','validated_steps'])
        payload = rb.payload_for(draft, source, body, 'Synthetic Runbook')
        rendered = rb.rendered_body(payload); digest = hashlib.sha256(rendered.encode()).hexdigest()
        meta = dict(maintainer_id=actor,origin='human',actor_id=actor,visibility='workspace',environment_ids=['test'])
        before = dict(documents=[],runbooks=[],revisions=[],history=[],audit=[{'id':1,'action':'v3.upload.created'}],
                      uploads=[{'id':source,'sha256':hashlib.sha256(body).hexdigest()}],revocations=[])
        after = copy.deepcopy(before)
        doc = dict(id=uid,application_id=None,title=payload['title'],body=rendered,source='runbook://v3/'+uid,
            version=1,content_hash=digest,created_by=actor,lifecycle='draft',metadata=meta,active_generation=None,
            created_at='2026-10-05T01:00:00.123456+00:00')
        after['documents'] = [doc]
        after['runbooks'] = [dict(document_id=uid,source_kind='import',source_ref=source,conversation_id=None,
            steps=payload['steps'],source_snapshot=dict(source_id=source,source_kind='import',
            content_sha256=payload['expected_source_sha256']),reviewers=[],when_text='',last_verified_at=None)]
        snapshot = dict(doc,application_id='')
        after['revisions'] = [dict(document_id=uid,version=1,created_by=actor,snapshot=snapshot,created_at=doc['created_at'])]
        after['history'] = [dict(id=1,document_id=uid,version=1,actor_id=actor,operation='v3.runbook.created')]
        after['audit'].append(dict(id=2,actor_id=actor,application_id='',action='runbook.created',target=uid,
            outcome='succeeded',changes=dict(status='draft',source='import',environment_id='test')))
        detail = dict(id=uid,title=payload['title'],version='v1',status='draft',base='import:'+source,
            source_kind='import',source_ref=source,owner='Synthetic owner',scope='workspace · env:test',
            environment_ids=['test'],application_id='',team_id='',tags=[],when='',steps=payload['steps'],reviewers=[],
            versions=[dict(v='v1',who='Synthetic owner',when='2026-10-05T01:00:00Z')],
            usage=dict(refs=None,resolve_rate=None,weekly=None,highlight=None),incidents=None,last_verified=None,
            complete_v3_dto=False,partial=True,unknown_fields=['usage.refs','usage.resolve_rate','usage.weekly',
            'usage.highlight','incidents','pending_reviewers','execution_verification','pending_reviewers'])
        return actor, uid, source, body, draft, payload, before, after, detail

    def test_private_ports_leave_shared_and_upload_helpers_unchanged(self):
        self.assertIsNot(rb.ro, shared)
        self.assertIsNot(rb.ro, rb.upload_util.ro)
        self.assertEqual(shared.PORTS, (18602,18603))
        self.assertEqual(rb.upload_util.ro.PORTS, (18726,18727))
        self.assertEqual(rb.ro.PORTS, (18742,18743))

    def test_opt_in_precedes_resources(self):
        with (mock.patch.object(sys,'argv',['runbook']),mock.patch.object(rb,'doctor') as doctor,
              mock.patch.object(rb.os,'open') as opened,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit): rb.main()
        doctor.assert_not_called(); opened.assert_not_called()

    def test_inventory_allows_only_exact_import_query_and_bounded_routes(self):
        contract = rb.Contract(rb.ROOT)
        for method,path in (*rb.TARGETS,*rb.SUPPORT):
            contract.canonical(method,'/api/v1'+path.replace('{id}','a'*32))
        contract.canonical('GET','/api/v1/runbooks/draft?source=import&from='+'a'*32)
        for method,path in [('POST','/api/v1/runbooks/'+'a'*32+'/submit'),('POST','/api/v1/incidents'),
            ('GET','https://remote.invalid/api/v1/runbooks/draft'),('GET','/api/v1/runbooks/draft?source=blank&from='+'a'*32),
            ('GET','/api/v1/runbooks/draft?source=import&from='+'a'*32+'&from='+'b'*32),
            ('GET','/api/v1/runbooks/'+'a'*32+'?source=import&from='+'a'*32)]:
            with self.assertRaises((rb.Blocked,AssertionError)): contract.canonical(method,path)

    def test_identity_pg_cpu_memory_and_source_fail_closed(self):
        data=dict(source_tree_sha=rb.SOURCE_SHA,binary_sha256=rb.BINARY_SHA,runtime_sha=rb.RUNTIME_SHA)
        rb.frozen_identity(data,rb.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError): rb.frozen_identity(dict(data,**{key:'0'*64}),rb.PG_CID)
        info=dict(Id=rb.PG_CID,HostConfig=dict(NanoCpus=4_000_000_000,Memory=2<<30))
        with mock.patch.object(rb,'_inspect_pg',return_value=info): rb.inspect_pg('owned')
        for bad in [dict(info,Id='other'),dict(info,HostConfig=dict(NanoCpus=2_000_000_000,Memory=2<<30)),
                    dict(info,HostConfig=dict(NanoCpus=4_000_000_000,Memory=1<<30))]:
            with mock.patch.object(rb,'_inspect_pg',return_value=bad):
                with self.assertRaises(AssertionError): rb.inspect_pg('owned')

    def test_source_drift_stops_http(self):
        chain=rb.Chain.__new__(rb.Chain);chain.frozen=mock.Mock(side_effect=rb.Blocked('source drift'))
        with mock.patch.object(rb.ro.Chain,'call') as call:
            with self.assertRaises(rb.Blocked): chain.call('reader','POST','/api/v1/runbooks')
        call.assert_not_called()

    def test_formal_contract_and_schema_match_candidate_inputs_and_ledger(self):
        source=(rb.ROOT/'internal/v3knowledge/runbooks.go').read_text()
        create=source[source.index('func (s *Service) createHandler'):source.index('func (s *Service) submitHandler')]
        self.assertIn('defer tx.Rollback(r.Context())',create)
        for table in ('knowledge_documents','v3_runbooks','knowledge_revisions','knowledge_history','management_audit'):
            self.assertIn('INSERT INTO '+table,create)
        self.assertLess(create.index('INSERT INTO management_audit'),create.index('tx.Commit'))
        for name in ('title','source','from','expected_source_sha256','steps','status'):
            self.assertIn('json:"'+name,source)
        self.assertNotIn('ConversationID',source[source.index('type createInput struct'):source.index('func (s *Service) createHandler')])
        draft=(rb.ROOT/'internal/v3knowledge/draft_source.go').read_text()
        self.assertIn('Title: "Review imported Markdown", Body: markdown, Readonly: true',draft)
        self.assertIn('binding.ContentHash = store.Hash(string(content))',draft)
        self.assertIn('source_snapshot', (rb.ROOT/'migrations/132_v3_runbook_source_snapshot.sql').read_text())
        self.assertIn('id text PRIMARY KEY', (rb.ROOT/'migrations/135_v3_upload_source_revoke.sql').read_text())

    def test_draft_rejects_wrong_digest_source_or_executable_steps(self):
        _,_,source,body,draft,_,*_=self.fixture()
        for bad in [dict(draft,source_sha256='0'*64),dict(draft,sources=['import:'+'d'*32]),
                    dict(draft,steps=[dict(n=1,kind='action',title='Execute guessed command',body=body.decode())])]:
            with self.assertRaises(AssertionError): rb.payload_for(bad,source,body,'Synthetic')

    def test_rollback_detects_each_partial_table_write_and_source_mutation(self):
        *_,before,after,_=self.fixture()
        rb.verify_rollback(before,copy.deepcopy(before))
        for table in ('documents','runbooks','revisions','history','audit','uploads','revocations'):
            partial=copy.deepcopy(before)
            partial[table].append({'id':'residual','partial':True})
            with self.subTest(table=table), self.assertRaises(AssertionError): rb.verify_rollback(before,partial)
        altered=copy.deepcopy(before);altered['uploads'][0]['sha256']='0'*64
        with self.assertRaises(AssertionError): rb.verify_rollback(before,altered)

    def test_recovery_rejects_missing_duplicate_or_wrong_lineage(self):
        actor,uid,_,_,_,payload,before,after,_=self.fixture()
        rb.verify_recovery(before,after,uid,actor,payload)
        for table in ('documents','runbooks','revisions','history','audit'):
            for replacement in ([],after[table]+[copy.deepcopy(after[table][-1])]):
                bad=copy.deepcopy(after);bad[table]=replacement
                with self.subTest(table=table),self.assertRaises(AssertionError): rb.verify_recovery(before,bad,uid,actor,payload)
        for table,field,value in [('documents','body','wrong'),('runbooks','source_ref','d'*32),
                                  ('history','operation','wrong'),('audit','outcome','success')]:
            bad=copy.deepcopy(after);bad[table][-1][field]=value
            with self.assertRaises(AssertionError): rb.verify_recovery(before,bad,uid,actor,payload)
        bad=copy.deepcopy(after);bad['revisions'][0]['snapshot']['body']='partial'
        with self.assertRaises(AssertionError): rb.verify_recovery(before,bad,uid,actor,payload)
        bad=copy.deepcopy(after);bad['uploads'][0]['sha256']='0'*64
        with self.assertRaises(AssertionError): rb.verify_recovery(before,bad,uid,actor,payload)

    def test_detail_requires_exact_steps_bytes_partial_unknown_scope_and_revision_time(self):
        _,uid,_,_,_,payload,_,after,detail=self.fixture()
        rb.verify_detail(detail,uid,payload,'Synthetic owner',after['revisions'][0])
        for key,value in [('complete_v3_dto',True),('usage',dict(refs=0,resolve_rate=0,weekly=[],highlight='')),
                          ('environment_ids',['staging']),('source_ref','d'*32),('steps',[]),('unknown_fields',[])]:
            bad=copy.deepcopy(detail);bad[key]=value
            with self.assertRaises(AssertionError): rb.verify_detail(bad,uid,payload,'Synthetic owner',after['revisions'][0])
        bad=copy.deepcopy(detail);bad['versions'][0]['when']='2026-10-06T01:00:00Z'
        with self.assertRaises(AssertionError): rb.verify_detail(bad,uid,payload,'Synthetic owner',after['revisions'][0])

    def test_audit_trigger_is_exact_actor_import_draft_environment_and_owned_names(self):
        name,create,drop=rb.fault_statements('a'*32,'b'*32)
        for clause in ("NEW.action='runbook.created'", "NEW.actor_id='"+'b'*32+"'",
                       "NEW.changes->>'environment_id'='test'", "NEW.changes->>'source'='import'", "NEW.changes->>'status'='draft'"):
            self.assertIn(clause,create)
        self.assertIn('DROP TRIGGER IF EXISTS '+name+' ON public.management_audit',drop)
        self.assertIn('DROP FUNCTION IF EXISTS public.'+name+'()',drop)
        with self.assertRaises(AssertionError): rb.fault_statements('bad',"x';DROP TABLE users;--")

    def test_ledger_reads_all_rows_with_actual_keys_and_no_business_sql(self):
        chain=rb.Chain.__new__(rb.Chain);chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.sql=mock.Mock(return_value='{}')
        chain.ledger();query=chain.sql.call_args.args[0]
        self.assertTrue(chain.sql.call_args.kwargs['read'])
        self.assertNotIn('WHERE',query)
        self.assertNotIn('ORDER BY upload_id',query)
        self.assertIn("to_jsonb(t)-'content'",query)
        for table in ('knowledge_documents','v3_runbooks','knowledge_revisions','knowledge_history','management_audit'):
            self.assertIn('FROM '+table+' t',query)

    def test_restore_one_four_second_deadline_and_confirm_absence(self):
        chain=rb.Chain.__new__(rb.Chain);chain.nonce='a'*32;chain.users={'reader':'b'*32};chain.fault_attempted=True
        chain.database='owned-db';chain.db_user='owned-role';chain.owner=mock.Mock()
        chain.pg=mock.Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
        with mock.patch.object(rb.time,'monotonic',return_value=100): chain.remove_fault()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual([c.kwargs['deadline'] for c in chain.pg.call_args_list],[104,104])
        self.assertFalse(chain.fault_attempted)

    def test_uncertain_fault_installation_retains_restoration_obligation(self):
        chain=rb.Chain.__new__(rb.Chain);chain.nonce='a'*32;chain.users={'reader':'b'*32};chain.fault_attempted=False
        chain.frozen=mock.Mock();chain.owner=mock.Mock();chain.sql=mock.Mock(side_effect=rb.Blocked('outcome unknown'))
        with self.assertRaises(rb.Blocked): chain.install_fault()
        self.assertTrue(chain.fault_attempted)

    def test_cleanup_parent_always_runs_if_fault_restore_fails(self):
        chain=rb.Chain.__new__(rb.Chain);chain.remove_fault=mock.Mock(side_effect=RuntimeError('restore failed'))
        with (mock.patch.object(rb.ro.Chain,'cleanup') as cleanup,mock.patch.object(rb.socket,'socket') as socket):
            socket.return_value.__enter__.return_value.connect_ex.return_value=1
            with self.assertRaises(RuntimeError): chain.cleanup()
        cleanup.assert_called_once()

    def test_outer_execute_cleans_even_if_business_or_reporting_fails(self):
        chain=mock.Mock();chain.run.side_effect=RuntimeError('business failed');chain.first_red.side_effect=OSError('report failed')
        with mock.patch.object(rb.ro,'emit',side_effect=OSError('stdout failed')):
            with self.assertRaises(RuntimeError): rb.ro.execute(chain)
        chain.cleanup.assert_called_once()

    def test_partial_rollback_stops_recovery_and_restores_fault(self):
        actor,_,source,body,draft,payload,before,after,_=self.fixture()
        # run() generates its own body/title; return its valid draft at the call boundary.
        chain=rb.Chain.__new__(rb.Chain);chain.nonce='d'*32;chain.marker='synthetic';chain.users={'reader':actor}
        for name in ('provision','start','seed','login','source_bytes','marker_case','install_fault','remove_fault'):
            setattr(chain,name,mock.Mock())
        real_body=('# inert Runbook source\n'+chain.nonce+'\n').encode()
        draft.update(source_sha256=hashlib.sha256(real_body).hexdigest(),steps=[dict(n=1,kind='check',
            title='Review imported Markdown',body=real_body.decode(),readonly=True)])
        before['uploads'][0].update(owner_id=actor,sha256=hashlib.sha256(real_body).hexdigest())
        chain.upload=mock.Mock(return_value={'id':source});chain.call=mock.Mock(side_effect=[(draft,{}),({}, {})])
        chain.ledger=mock.Mock(side_effect=[before,after])
        with self.assertRaises(AssertionError): chain.run()
        chain.remove_fault.assert_called_once()
        self.assertEqual(chain.call.call_count,2)
        self.assertEqual(chain.call.call_args.kwargs['want'],503)


if __name__=='__main__': unittest.main()
