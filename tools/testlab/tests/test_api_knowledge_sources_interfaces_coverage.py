import contextlib
import datetime
import io
import re
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_knowledge_sources_interfaces_coverage as knowledge

class KnowledgeInterfaceTests(unittest.TestCase):
    def test_private_lifecycle_keeps_shared_globals(self):
        self.assertIsNot(knowledge.ro,shared)
        self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(knowledge.ro.PORTS,(18714,18715))

    def test_inventory_excludes_sync_model_and_full_chain(self):
        contract=knowledge.Contract(knowledge.ROOT)
        for method,path in (*knowledge.TARGETS,*knowledge.SUPPORT):
            contract.canonical(method,'/api/v1'+path.replace('{id}','a'*32))
        for method,path in [('POST','/knowledge-sources/registrations/x/sync'),
                            ('POST','/conversations'),('POST','/postmortems'),
                            ('POST','/runbooks'),('POST','/incidents')]:
            with self.assertRaises(knowledge.Blocked): contract.canonical(method,'/api/v1'+path)
        with self.assertRaises(knowledge.Blocked):
            contract.canonical('GET','https://external.invalid/api/v1/knowledge-sources')

    def test_every_identity_fails_closed(self):
        data=dict(source_tree_sha=knowledge.SOURCE_SHA,binary_sha256=knowledge.BINARY_SHA,
                  runtime_sha=knowledge.RUNTIME_SHA)
        knowledge.frozen_identity(data,knowledge.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError):
                knowledge.frozen_identity(dict(data,**{key:'0'*64}),knowledge.PG_CID)
        with self.assertRaises(AssertionError): knowledge.frozen_identity(data,'0'*64)

    def test_opt_in_precedes_resource_acquisition(self):
        with (mock.patch.object(sys,'argv',['knowledge']),mock.patch.object(knowledge,'doctor') as doctor,
              mock.patch.object(knowledge.os,'open') as opened,contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit): knowledge.main()
        doctor.assert_not_called(); opened.assert_not_called()

    def test_every_pg_inspection_rechecks_exact_container(self):
        with mock.patch.object(knowledge,'_inspect_pg',return_value={'Id':knowledge.PG_CID}) as inspect:
            self.assertEqual(knowledge.ro.inspect_pg('owned',timeout=3),{'Id':knowledge.PG_CID})
            inspect.assert_called_once_with('owned',timeout=3)
        with mock.patch.object(knowledge,'_inspect_pg',return_value={'Id':'different'}):
            with self.assertRaises(AssertionError): knowledge.ro.inspect_pg('owned')

    def test_source_drift_prevents_next_http_request(self):
        chain=knowledge.Chain.__new__(knowledge.Chain)
        chain.frozen=mock.Mock(side_effect=knowledge.Blocked('source drift'))
        with mock.patch.object(knowledge.ro.Chain,'call') as call:
            with self.assertRaises(knowledge.Blocked): chain.call('reader','GET','/api/v1/knowledge-sources')
        call.assert_not_called()

    def test_probe_wire_payload_obeys_actual_strict_connection_request(self):
        source=(knowledge.ROOT/'internal/v3sourceregistry/service.go').read_text()
        request=source.split('type connectionRequest struct {',1)[1].split('\n}',1)[0]
        fields=set(re.findall(r'json:"([a-z_]+)"',request))
        self.assertEqual(fields,{'type','url','secret_ref','spaces','application_id','team_id'})
        payload=knowledge.registration('owned-app','/api/v1/uploads/'+'a'*32+'/content','private-title')
        self.assertEqual(set(payload)-fields,{'name','schedule','chunk','inherit_acl'})
        chain=knowledge.Chain.__new__(knowledge.Chain); chain.frozen=mock.Mock()
        captured=[]
        def strict_call(user,method,path,body,**kwargs):
            captured.append(body)
            self.assertFalse(set(body)-fields,'actual DisallowUnknownFields rejects registration-only fields with invalid_body')
            self.assertEqual(kwargs['want'],403)
            return {'error':{'code':'forbidden'}},{}
        with mock.patch.object(knowledge.ro.Chain,'call',side_effect=strict_call):
            chain.call('denied','POST','/api/v1/knowledge-sources/test',payload,want=403,code='forbidden')
        self.assertEqual(captured[0],{key:value for key,value in payload.items() if key in fields})
        self.assertIn('name',payload)

    def test_registration_payload_is_preserved_including_boundary_values(self):
        chain=knowledge.Chain.__new__(knowledge.Chain); chain.frozen=mock.Mock()
        payload=knowledge.registration('app','upload://intentionally-invalid','')
        with mock.patch.object(knowledge.ro.Chain,'call',return_value=({},{})) as call:
            chain.call('reader','POST','/api/v1/knowledge-sources',payload,want=422)
        self.assertIs(call.call_args.args[3],payload)
        self.assertEqual(set(payload),{'name','type','url','secret_ref','spaces','schedule','chunk','inherit_acl','application_id'})

    def test_multipart_rejects_unsafe_uuid_body_and_delimiter(self):
        raw,kind,name=knowledge.multipart('a'*32,b'# inert\n')
        self.assertIn(b'filename="knowledge-'+b'a'*32+b'.md"',raw)
        self.assertTrue(raw.endswith(b'--\r\n')); self.assertIn('boundary=knowledge-',kind)
        self.assertEqual(name,'knowledge-'+'a'*32+'.md')
        for nonce,body in [('bad',b'a'),('a'*32,b''),('a'*32,b'x'*131073),
                           ('a'*32,b'knowledge-'+b'a'*32)]:
            with self.assertRaises(AssertionError): knowledge.multipart(nonce,body)
        with self.assertRaises(UnicodeDecodeError): knowledge.multipart('a'*32,b'\xff')

    def test_upload_transport_scoped_and_restored_on_failure(self):
        chain=knowledge.Chain.__new__(knowledge.Chain)
        chain.nonce='a'*32; chain.cookies={'reader':'aiops_session=private'}
        captured=[]
        def transport(*args,**kwargs): captured.append(args); raise RuntimeError('bounded')
        def call(user,method,path,body,**kwargs):
            return knowledge.ro.transport(knowledge.ro.BASES[0],method,path,b'{}',
                {'Cookie':chain.cookies[user],'X-AIOps-Environment':'test','Content-Type':'application/json'})
        chain.call=call
        with mock.patch.object(knowledge.ro,'transport',side_effect=transport) as original:
            with self.assertRaises(RuntimeError): chain.upload('reader',b'# upload\n')
            self.assertIs(knowledge.ro.transport,original)
        self.assertIn(b'# upload\n',captured[0][3])
        self.assertTrue(captured[0][4]['Content-Type'].startswith('multipart/form-data;'))
        self.assertNotIn('Authorization',captured[0][4])

    def test_upload_substitution_cannot_escape_target_or_actor(self):
        chain=knowledge.Chain.__new__(knowledge.Chain)
        chain.nonce='a'*32; chain.cookies={'reader':'aiops_session=owned'}
        for path,cookie in [('/api/v1/knowledge-sources','aiops_session=owned'),
                            ('/api/v1/uploads','aiops_session=foreign')]:
            chain.call=lambda *args,**kwargs: knowledge.ro.transport(knowledge.ro.BASES[0],'POST',path,b'{}',
                {'Cookie':cookie,'X-AIOps-Environment':'test'})
            with mock.patch.object(knowledge.ro,'transport') as original:
                with self.assertRaises(AssertionError): chain.upload('reader',b'# inert\n')
                self.assertIs(knowledge.ro.transport,original); original.assert_not_called()

    def honest_dto(self):
        item=dict(id='source',kind='upload',application_id='app',environment_id='test',registration_status='unverified')
        item.update({key:None for key in ('last_read','keyword_content','connection_status','sync_status',
                    'document_count','chunk_count','last_sync_at','index_status','error')})
        return dict(items=[item],index=None,complete_v3_dto=True,partial=True,truncated=False,
                    reason='scoped_source_evidence_partial',
                    unknown_fields=['connection_status','sync_status','index_status','index_settings'])

    def test_evidence_fails_on_false_health_index_scope_or_secret(self):
        knowledge.evidence(self.honest_dto(),['source'],'app')
        for patch in ({'index_status':'ready'},{'connection_status':'connected'},{'secret_ref':'private'},
                      {'application_id':'foreign'},{'last_read':{'id':'false-read'}}):
            value=self.honest_dto(); value['items'][0].update(patch)
            with self.assertRaises(AssertionError): knowledge.evidence(value,['source'],'app')
        with self.assertRaises(AssertionError): knowledge.evidence(self.honest_dto(),[],'app')

    def test_probe_requires_fresh_nonpersistent_exact_result(self):
        value=dict(ok=True,message='Read-only source access succeeded',connected=True,
                   observed_at=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   persistent=False,scope_verified=False)
        knowledge.probe_result(value)
        for patch in ({'persistent':True},{'scope_verified':True},{'observed_at':'2000-01-01T00:00:00Z'},
                      {'source_url':'private'}):
            with self.assertRaises(AssertionError): knowledge.probe_result(dict(value,**patch))

    def test_probe_audit_requires_exact_actor_scope_prefix_and_no_persistence(self):
        chain=knowledge.Chain.__new__(knowledge.Chain); chain.users={'reader':'owner'}
        before={'content':[],'audits':[{'old':'audit'}]}
        rows=[dict(action='v3.knowledge_source.connection.test',actor_id='owner',application_id='app',
                   target='upload',outcome=outcome,changes={'environment_id':'test','team_id':''})
              for outcome in ('attempted','success')]
        after={'content':[],'audits':before['audits']+rows}
        chain.assert_new_audits(before,after,['attempted','success'],'app')
        for changed in [dict(after,content=['fabricated']),dict(after,audits=rows),
                        dict(after,audits=before['audits']+list(reversed(rows)))]:
            with self.assertRaises(AssertionError): chain.assert_new_audits(before,changed,['attempted','success'],'app')
        rows[1]['changes']['secret_ref']='private'
        with self.assertRaises(AssertionError): chain.assert_new_audits(before,after,['attempted','success'],'app')

    def test_audit_failure_scoped_only_to_owned_registration(self):
        chain=knowledge.Chain.__new__(knowledge.Chain); chain.users={'reader':'owned'}; chain.sql=mock.Mock()
        chain.audit_fault(True)
        sql=chain.sql.call_args.args[0]
        self.assertIn("NEW.actor_id='owned'",sql)
        self.assertIn("NEW.action='v3.knowledge_source.registered'",sql)
        self.assertNotIn('connection.test',sql); self.assertNotIn('INSERT INTO',sql)
        chain.audit_fault(False); self.assertIn('DROP TRIGGER',chain.sql.call_args.args[0])

    def test_rejection_state_write_blocks_next_progress(self):
        chain=knowledge.Chain.__new__(knowledge.Chain); chain.call=mock.Mock()
        chain.ledger=mock.Mock(side_effect=[{'audits':[]},{'audits':['unexpected']}])
        with self.assertRaisesRegex(AssertionError,'changed source/audit state'):
            chain.reject('peer','POST','/api/v1/knowledge-sources',{},403,'forbidden')
        chain.call.assert_called_once()

    def test_cleanup_failure_never_emits_pass(self):
        chain=knowledge.Chain.__new__(knowledge.Chain)
        with (mock.patch.object(knowledge.ro.Chain,'cleanup',side_effect=knowledge.Blocked('foreign session')),
              mock.patch.object(knowledge,'emit') as emit,mock.patch.object(knowledge.socket,'socket') as socket):
            with self.assertRaises(knowledge.Blocked): chain.cleanup()
        emit.assert_not_called(); socket.assert_not_called()

if __name__ == '__main__': unittest.main()
