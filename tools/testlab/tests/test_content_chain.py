from __future__ import annotations
import hashlib,hmac,json,os,pathlib,re,signal,sys,tempfile,types,unittest,uuid
from unittest.mock import patch
from email.parser import BytesParser
from email import policy
TOOLS=pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0,str(TOOLS))
import content_chain as c

class ContentChainOfflineTests(unittest.TestCase):
    def setUp(self):
        self.directory=tempfile.TemporaryDirectory();self.addCleanup(self.directory.cleanup)
        self.root=pathlib.Path(self.directory.name);self.owner='testlab-123456789abc'
        self.data={'owner':self.owner,'source_tree_sha':'source','binary_sha256':'binary','control_secret':'private-secret','supervisor_pid':123,
                   'binary':str(self.root/'aiops'),'postgres_container':'aiops-'+self.owner+'-pg','postgres_image_id':'sha256:owned',
                   'admin':{'email':'admin@testlab.invalid','password':'private-password'}}
    def receipt(self,nonce='1'*32):
        identity={'owner':self.owner,'pid':123,'api_pids':[124,125],'source_tree_sha':'source','ready':True}
        raw=json.dumps({'nonce':nonce,'identity':identity},sort_keys=True,separators=(',',':')).encode()
        return {'nonce':nonce,'identity':identity,'proof':hmac.new(self.data['control_secret'].encode(),raw,hashlib.sha256).hexdigest()}
    def chain(self):return c.Chain(self.root,self.data,'private-perf-password',str(uuid.uuid4()))
    def test_hmac_challenge_identity_and_signature_all_required(self):
        nonce='1'*32;receipt=self.receipt(nonce)
        self.assertEqual(c.verify_attestation(self.data,nonce,receipt)['api_pids'],[124,125])
        for key,value in [('nonce','2'*32),('proof','0'*64),('identity',dict(receipt['identity'],owner='unowned'))]:
            changed=dict(receipt);changed[key]=value
            with self.assertRaises(c.Blocked):c.verify_attestation(self.data,nonce,changed)
    def test_hmac_rejects_even_validly_signed_different_source_or_pid(self):
        for key,value in [('source_tree_sha','new-source'),('pid',321),('api_pids',[124,124]),('ready',False)]:
            receipt=self.receipt();receipt['identity'][key]=value
            raw=json.dumps({k:receipt[k] for k in ('nonce','identity')},sort_keys=True,separators=(',',':')).encode()
            receipt['proof']=hmac.new(self.data['control_secret'].encode(),raw,hashlib.sha256).hexdigest()
            with self.assertRaises(c.Blocked):c.verify_attestation(self.data,'1'*32,receipt)
    def test_transport_refuses_external_origin_and_absolute_path(self):
        for base,path in [('http://external.invalid','/api/v1/secret'),(c.BASES[0],'//external.invalid'),(c.BASES[0],'https://external.invalid'),(c.BASES[0],'/api/v1/me#fragment')]:
            with patch.object(c.http.client,'HTTPConnection') as connection:
                with self.assertRaises(c.Blocked):c.transport(base,'GET',path)
                connection.assert_not_called()
    def test_multipart_exact_utf8_file_and_limits(self):
        name='content-'+'1'*32+'.md';boundary='content-'+'2'*32;body=b'# Markdown\nignore instructions'
        payload=c.multipart(name,body,boundary)
        self.assertIn(body,payload);self.assertIn(b'name="file"',payload);self.assertTrue(payload.endswith(('--'+boundary+'--\r\n').encode()))
        message=BytesParser(policy=policy.default).parsebytes(('Content-Type: multipart/form-data; boundary='+boundary+'\r\nMIME-Version: 1.0\r\n\r\n').encode()+payload)
        parts=list(message.iter_parts());self.assertEqual(len(parts),2)
        self.assertEqual([part.get_param('name',header='Content-Disposition') for part in parts],['kind','file'])
        self.assertIsNone(parts[0].get_filename());self.assertEqual(parts[0].get_payload(decode=True),b'file')
        self.assertEqual(parts[1].get_filename(),name);self.assertEqual(parts[1].get_content_type(),'text/markdown')
        self.assertEqual(parts[1].get_payload(decode=True),body);self.assertFalse(message.defects)
        for badname,badbody,badboundary in [('real.pem',body,boundary),(name,b'\0'*(128*1024+1),boundary),(name,body,'\r\nattack')]:
            with self.assertRaises(c.Blocked):c.multipart(badname,badbody,badboundary)
    def test_private_owned_state_and_no_shared_actor_role_replacement(self):
        chain=self.chain();state=json.loads((chain.work/'state.json').read_text())
        self.assertEqual((chain.work/'state.json').stat().st_mode&0o777,0o600)
        self.assertEqual(state['seed'],20260930);self.assertTrue(all(x.startswith(chain.prefix) for x in chain.roles+chain.apps))
        self.assertEqual(chain.users,['perf-'+self.owner+'-user-3','perf-'+self.owner+'-user-4'])
    def test_run_id_rejects_noncanonical_uuid_and_path(self):
        for run in ['../unsafe','not-a-uuid','ABCDEFAB-1234-4234-8234-123456789ABC']:
            with self.assertRaises((ValueError,c.Blocked)):c.Chain(self.root,self.data,'secret',run)
    def test_sql_refuses_changed_pg_owner_or_image_before_exec(self):
        chain=self.chain()
        for info in [{'Name':'/'+self.data['postgres_container'],'Image':'sha256:other','State':{'Running':True},'Config':{'Labels':{'aiops.testlab.owner':self.owner}}},
                     {'Name':'/'+self.data['postgres_container'],'Image':'sha256:owned','State':{'Running':True},'Config':{'Labels':{'aiops.testlab.owner':'unowned'}}}]:
            with patch.object(chain,'frozen'),patch.object(c,'_inspect',return_value=info),patch.object(c.subprocess,'run') as run:
                with self.assertRaises((c.Blocked,ValueError)):chain.sql('SELECT 1',read=True)
                run.assert_not_called()
    def test_red_is_exclusive_and_secret_response_not_in_public_output(self):
        chain=self.chain();chain.remember('source','source-private-id')
        with patch.object(chain,'frozen'),patch.object(c,'transport',return_value=(503,b'{"private":"body-secret","error":{"code":"source_unavailable"}}',{})),patch.object(c,'emit') as emit:
            with self.assertRaises(AssertionError):chain.call(0,'POST','/api/v1/knowledge-sources/registrations/source-private-id/sync',{})
            self.assertNotIn('body-secret',str(emit.call_args));self.assertNotIn('source-private-id',str(emit.call_args))
        first=(chain.work/'first-failure.json').read_text();chain.first_red({'replacement':True})
        self.assertEqual((chain.work/'first-failure.json').read_text(),first)
        self.assertEqual((chain.work/'first-failure.json').stat().st_mode&0o777,0o600)
        self.assertIn('body-secret',(chain.work/'http-1.json').read_text())
    def test_normal_and_error_interfaces_not_aliased_or_counted_pass_on_error(self):
        chain=self.chain()
        with patch.object(chain,'frozen'),patch.object(c,'transport',return_value=(404,b'{"error":{"code":"not_found"}}',{})),patch.object(c,'emit') as emit:
            chain.call(1,'GET','/api/v1/knowledge-sources',want=404,scenario='authorization')
            row=emit.call_args.kwargs['interface'];self.assertFalse(row['registration_evidence']);self.assertEqual(row['scenario'],'authorization')
        with patch.object(chain,'frozen'),patch.object(c,'transport',return_value=(200,b'not json',{})),patch.object(c,'emit'):
            with self.assertRaises(AssertionError):chain.call(0,'GET','/api/v1/knowledge-sources')
    def test_existing_negative_actor_grants_block_without_removing_other_work(self):
        chain=self.chain()
        with patch.object(chain,'login'),patch.object(chain,'sql',return_value='[{"module":"knowledge","scope_kind":"global","scope_id":"*"}]') as sql:
            with self.assertRaises(c.Blocked):chain.setup()
            self.assertEqual(sql.call_count,1);self.assertFalse(chain.seeded)
    def test_seed_roles_satisfy_unique_name_constraint_across_actors_and_runs(self):
        names=[]
        for _ in range(2):
            chain=self.chain()
            with patch.object(chain,'login'),patch.object(chain,'sql',side_effect=['[]','']) as sql,patch.object(c,'emit'):
                chain.setup()
                rows=re.findall(r"INSERT INTO rbac_roles\(id,name\) VALUES\('([^']+)','([^']+)'\)",sql.call_args.args[0])
                self.assertEqual(len(rows),2)
                self.assertEqual([row[0] for row in rows],chain.roles)
                self.assertTrue(all(name.startswith(chain.prefix) for _,name in rows))
                names.extend(name for _,name in rows)
        self.assertEqual(len(names),len(set(names)), 'rbac_roles.name has a schema-level UNIQUE constraint')
    def test_synthetic_scope_only_mutates_owned_unpublished_matching_draft(self):
        chain=self.chain();rb='a'*32;chain.remember('runbook',rb)
        confirmation={'application_id':chain.apps[0],'visibility':'application','lifecycle':'draft','version':1,
                      'snapshot_application_id':chain.apps[0],'snapshot_metadata_matches':True,'snapshot_lifecycle':'draft'}
        with patch.object(chain,'sql',return_value=json.dumps(confirmation)) as sql:
            chain.bind_synthetic_runbook_draft(rb)
            statement=sql.call_args.args[0]
            for guard in ["d.lifecycle='draft'",'d.version=1',"d.created_by='"+chain.users[0]+"'", "d.application_id IS NULL",
                          "kr.snapshot->>'lifecycle'='draft'", "kr.snapshot->'metadata'=d.metadata", "d.id='"+rb+"'",
                          "a.id='"+chain.apps[0]+"'", 'IF changed<>1 THEN RAISE EXCEPTION']:
                self.assertIn(guard,statement)
            self.assertNotIn("SET lifecycle",statement);self.assertNotIn('v3_runbook_reviews',statement)
            self.assertNotIn('v3_role_grants',statement);self.assertIn("'{application_id}'",statement);self.assertIn("'{metadata}'",statement)
        proof=json.loads((chain.work/'synthetic-runbook-input.json').read_text())
        self.assertEqual(proof['synthetic_input_scope'],'graph_and_application_bound_unpublished_runbook_draft')
        self.assertEqual(proof['confirmation']['lifecycle'],'draft')
        with patch.object(chain,'sql') as sql:
            with self.assertRaises(c.Blocked):chain.bind_synthetic_runbook_draft('b'*32)
            sql.assert_not_called()
        with patch.object(chain,'sql',return_value=json.dumps(dict(confirmation,lifecycle='published'))):
            with self.assertRaises(AssertionError):chain.bind_synthetic_runbook_draft(rb)
    def test_scope_input_precedes_formal_independent_approval_and_audit_readback(self):
        chain=self.chain();rb='a'*32;order=[]
        def call(user,method,path,body=None,**kwargs):
            order.append((user,path,kwargs.get('want',200)))
            if path=='/api/v1/runbooks':return {'id':rb},{}
            if method=='GET':return {'application_id':chain.apps[0]},{}
            return {'status':'published'},{}
        def bind(identifier):
            self.assertEqual(identifier,rb);order.append(('synthetic-draft',identifier,None))
        with patch.object(chain,'call',side_effect=call),patch.object(chain,'bind_synthetic_runbook_draft',side_effect=bind),patch.object(chain,'sql',return_value='{"reviews":1,"audits":1}') as sql:
            self.assertEqual(chain.publish_runbook('runbook',True),rb)
            self.assertTrue(sql.call_args.kwargs['read'])
        self.assertEqual(order[1][0],'synthetic-draft')
        self.assertTrue(order[2][1].endswith('/submit'))
        self.assertEqual(order[3][0],0);self.assertEqual(order[3][2],403)
        self.assertEqual(order[4][0],1);self.assertEqual(order[4][2],200)
    def test_cleanup_only_exact_run_roles_apps_and_completed_owned_graphs(self):
        chain=self.chain();chain.seeded=True;chain.completed=True;chain.ids={'canvas_steps':'CV-own','template_steps':'CT-own'}
        with patch.object(chain,'sql') as sql,patch.object(c,'emit'):
            chain.cleanup();statement=sql.call_args.args[0]
            self.assertNotIn('TRUNCATE',statement);self.assertNotIn("role_id='admin'",statement)
            self.assertIn("WHERE user_id='"+chain.users[0]+"' AND role_id='"+chain.roles[0]+"'",statement)
            for role in chain.roles:self.assertIn("DELETE FROM rbac_roles WHERE id='"+role+"' AND name='"+role+"';",statement)
            self.assertIn("owner_id='"+chain.users[0]+"'",statement);self.assertIn("environment_id='test'",statement)
            self.assertNotIn('DELETE FROM management_audit',statement)
    def test_failed_graphs_remain_for_investigation(self):
        chain=self.chain();chain.seeded=True
        with patch.object(chain,'sql') as sql,patch.object(c,'emit'):
            chain.cleanup();self.assertNotIn('DELETE FROM v3_canvases',sql.call_args.args[0])
    def test_cleanup_refuses_foreign_roles_and_application_ids(self):
        for field in ('roles','apps','users'):
            chain=self.chain();chain.seeded=True;setattr(chain,field,['foreign','foreign'])
            with patch.object(chain,'sql') as sql:
                with self.assertRaises(c.Blocked):chain.cleanup()
                sql.assert_not_called()
    def test_inert_sink_is_local_owned_and_counts_only_actual_calls(self):
        with c.InertSink() as sink:
            self.assertEqual(sink.hits,0);self.assertTrue(sink.url.startswith('http://127.0.0.1:'))
            parsed=c.urlsplit(sink.url);conn=c.http.client.HTTPConnection(parsed.hostname,parsed.port,timeout=2)
            try:conn.request('GET',parsed.path);response=conn.getresponse();response.read();self.assertEqual(response.status,204)
            finally:conn.close()
            self.assertEqual(sink.hits,1)
    def test_cleanup_failure_cannot_turn_business_success_into_pass(self):
        chain=self.chain()
        with patch.object(chain,'run',return_value={'green':True}),patch.object(chain,'cleanup',side_effect=c.Blocked('DB unavailable')),patch.object(c,'emit') as emit:
            self.assertEqual(c.execute(chain),2)
            self.assertFalse(any(x.args[:2]==('content.chain','passed') for x in emit.call_args_list))
            self.assertFalse(json.loads((chain.work/'exit.json').read_text())['cleanup_confirmed'])
    def test_sigterm_during_cleanup_keeps_finally_and_marks_interrupted(self):
        chain=self.chain()
        def cleanup():
            with c.interruption_cleanup():signal.raise_signal(signal.SIGTERM);c._private(chain.work/'cleanup.json',{'confirmed':True})
        previous=signal.getsignal(signal.SIGTERM)
        with patch.object(chain,'run',return_value={'green':True}),patch.object(chain,'cleanup',side_effect=cleanup),patch.object(c,'emit'):
            self.assertEqual(c.execute(chain),143)
        self.assertEqual(signal.getsignal(signal.SIGTERM),previous);self.assertTrue(json.loads((chain.work/'exit.json').read_text())['interrupted'])

    def probe_state(self):
        state={name:{'count':1,'digest':'a'*64} for name in c.UPLOAD_PROBE_TABLES}
        state['management_audit']['max_id']=100
        return state
    def probe_audits(self,chain,outcome='success'):
        return [{'id':identifier,'actor_id':chain.users[0],'actor_kind':'user','application_id':chain.apps[0],
                 'action':'v3.knowledge_source.connection.test','target':'upload','outcome':result,
                 'changes':{'environment_id':'test','team_id':''}} for identifier,result in ((101,'attempted'),(102,outcome))]
    def probe_response(self):
        from datetime import datetime,timezone
        return {'ok':True,'message':'Read-only source access succeeded','connected':True,
                'observed_at':datetime.now(timezone.utc).isoformat(),'persistent':False,'scope_verified':False}
    def probe_case(self,chain,response,before=None,after=None,want=200,code=None,audits=None):
        before=before if before is not None else self.probe_state()
        after=after if after is not None else before
        audits=audits if audits is not None else self.probe_audits(chain,'success' if want==200 else 'failure') if want in (200,503) else []
        with patch.object(chain,'frozen'),patch.object(chain,'upload_probe_state',side_effect=[before,after]), \
             patch.object(chain,'upload_probe_audit_rows',return_value=audits), \
             patch.object(c,'transport',return_value=(want,json.dumps(response).encode(),{})),patch.object(c,'emit') as emit:
            result=chain.upload_probe_case('normal',0,{'type':'upload','url':'/api/v1/uploads/'+'a'*32+'/content',
                         'application_id':chain.apps[0],'team_id':'','spaces':[]},want=want,code=code)
        return result,emit
    def test_probe_normal_exact_response_no_effects_and_formal_evidence(self):
        chain=self.chain();result,emit=self.probe_case(chain,self.probe_response())
        self.assertEqual(result['label'],'normal');self.assertTrue(result['state_unchanged'])
        row=emit.call_args.kwargs['interface'];self.assertEqual(row['path'],'/api/v1/knowledge-sources/test')
        self.assertEqual(row['method'],'POST');self.assertEqual(row['scenario'],'normal');self.assertTrue(row['registration_evidence'])
        self.assertEqual(row['status'],'passed');self.assertEqual((chain.work/'upload-probe-normal.json').stat().st_mode&0o777,0o600)
    def test_probe_does_not_accept_persistent_scope_or_empty_success(self):
        for changed in ({},dict(self.probe_response(),persistent=True),dict(self.probe_response(),scope_verified=True),
                        dict(self.probe_response(),connected=False),dict(self.probe_response(),observed_at='2000-01-01T00:00:00Z')):
            with self.subTest(response=changed),self.assertRaises(AssertionError):self.probe_case(self.chain(),changed)
    def test_probe_detects_same_count_state_mutation_every_table(self):
        for name in c.UPLOAD_PROBE_TABLES:
            before=self.probe_state();after=json.loads(json.dumps(before));after[name]['digest']='b'*64
            chain=self.chain()
            with self.subTest(table=name),self.assertRaises(AssertionError):self.probe_case(chain,self.probe_response(),before,after)
            self.assertEqual(json.loads((chain.work/'first-failure.json').read_text())['status'],'failed')
            row=json.loads((chain.work/'http-1.json').read_text())['interface']
            self.assertEqual(row['status'],'failed');self.assertIn('assertion',row['actual'])
    def test_probe_detects_new_read_registration_document_or_audit(self):
        for name in c.UPLOAD_PROBE_TABLES:
            before=self.probe_state();after=json.loads(json.dumps(before));after[name]['count']+=1
            with self.subTest(table=name),self.assertRaises(AssertionError):self.probe_case(self.chain(),self.probe_response(),before,after)
    def test_probe_failure_code_exact_and_no_success_claim(self):
        self.probe_case(self.chain(),{'error':{'code':'source_connection_failed'}},want=503,code='source_connection_failed')
        for response in ({'error':{'code':'other'}},{'ok':True,'error':{'code':'source_connection_failed'}}):
            with self.assertRaises(AssertionError):self.probe_case(self.chain(),response,want=503,code='source_connection_failed')
    def test_probe_state_is_read_only_all_tables_exact_and_no_body_output(self):
        chain=self.chain()
        with patch.object(chain,'sql',return_value=json.dumps(self.probe_state())) as sql:
            self.assertEqual(chain.upload_probe_state(),self.probe_state())
        statement=sql.call_args.args[0];self.assertTrue(sql.call_args.kwargs['read'])
        for name in c.UPLOAD_PROBE_TABLES:self.assertIn('FROM '+name+' t',statement)
        self.assertIn('sha256(',statement);self.assertIn('ORDER BY',statement)
        for forbidden in ('INSERT','UPDATE','DELETE','TRUNCATE'):self.assertNotIn(forbidden,statement)
        for malformed in ({},dict(self.probe_state(),management_audit={'count':1,'digest':'bad'})):
            with patch.object(chain,'sql',return_value=json.dumps(malformed)),self.assertRaises(AssertionError):chain.upload_probe_state()
    def test_probe_cases_are_bounded_no_sql_business_or_grants_and_old_cookie_revoked(self):
        chain=self.chain();chain.remember('upload','a'*32);chain.cookies[0]='aiops_session=old-cookie'
        calls=[];cases=[]
        def call(user,method,path,body=None,**kwargs):
            calls.append((user,method,path,kwargs))
            if path=='/api/v1/uploads':
                identifier=('b' if user=='admin' else 'c')*32
                return {'id':identifier,'url':'/api/v1/uploads/'+identifier+'/content'},{}
            return {'id':'c'*32,'revoked':True},{}
        def probe(label,user,payload,**kwargs):
            cases.append((label,user,payload,kwargs));return {'label':label,'state_unchanged':True,'audit_count':2 if label in ('normal','owner','environment','revoked_upload') else 0}
        with patch.object(chain,'call',side_effect=call),patch.object(chain,'upload_probe_case',side_effect=probe), \
             patch.object(chain,'login') as login,patch.object(chain,'sql') as sql,patch.object(c,'emit') as emit:
            chain.upload_probes('/api/v1/uploads/'+'a'*32+'/content')
        self.assertEqual([item[0] for item in cases],['normal','missing_scope','unauthenticated','permission','owner','environment','invalid_url','invalid_secret','revoked_upload','revoked_session'])
        byname={item[0]:item for item in cases}
        for label,user,payload,kwargs in cases:
            if label=='missing_scope':self.assertNotIn('application_id',payload);continue
            self.assertEqual(payload['application_id'],chain.apps[1] if label=='owner' else chain.apps[0])
            self.assertEqual(payload['team_id'],'');self.assertEqual(payload['spaces'],[])
        self.assertEqual(byname['normal'][3]['index'],1);self.assertEqual(byname['environment'][1],'admin')
        self.assertEqual(byname['environment'][3]['environment'],'test')
        self.assertEqual(byname['revoked_session'][1],0);login.assert_called_once();sql.assert_not_called()
        self.assertTrue(any(x[2].endswith('/revoke') for x in calls));self.assertTrue(any(x[2]=='/api/v1/auth/logout' for x in calls))
        self.assertEqual(chain.cookies[0],'aiops_session=old-cookie')
        self.assertEqual(sum(x.args[:2]==('content.upload_connection_probe','passed') for x in emit.call_args_list),1)

    def test_probe_exact_audit_identity_scope_order_and_no_missing_extra_or_secret(self):
        chain=self.chain();good=self.probe_audits(chain)
        variants=[[],good[:1],good+[dict(good[-1],id=103)],list(reversed(good)),[good[0],dict(good[1],id=101)]]
        for key,value in [('actor_id','another-actor'),('application_id',chain.apps[1]),('action','another.action'),
                          ('actor_kind','machine'),('target','notion'),('outcome','failure')]:
            changed=json.loads(json.dumps(good));changed[-1][key]=value;variants.append(changed)
        for changes in ({'environment_id':'staging','team_id':''},{'environment_id':'test','team_id':'foreign'},
                        {'environment_id':'test','team_id':'','url':'secret-origin'},
                        {'environment_id':'test','team_id':'','token':'private-token'}):
            changed=json.loads(json.dumps(good));changed[-1]['changes']=changes;variants.append(changed)
        for rows in variants:
            with self.subTest(rows=rows),self.assertRaises(AssertionError):self.probe_case(chain,self.probe_response(),audits=rows)
    def test_probe_early_scope_rejection_must_have_zero_audit_rows(self):
        chain=self.chain();response={'error':{'code':'invalid_source_scope'}}
        result,_=self.probe_case(chain,response,want=422,code='invalid_source_scope',audits=[])
        self.assertEqual(result['audit_count'],0)
        with self.assertRaises(AssertionError):self.probe_case(chain,response,want=422,code='invalid_source_scope',audits=self.probe_audits(chain))
    def test_probe_audit_reader_is_bound_readonly_and_old_prefix_digest_excludes_only_new_ids(self):
        chain=self.chain()
        with patch.object(chain,'sql',return_value='[]') as sql:
            self.assertEqual(chain.upload_probe_audit_rows(100),[])
        self.assertTrue(sql.call_args.kwargs['read']);self.assertIn('id>100',sql.call_args.args[0]);self.assertIn('LIMIT 3',sql.call_args.args[0])
        with patch.object(chain,'sql',return_value=json.dumps(self.probe_state())) as sql:chain.upload_probe_state(audit_cutoff=100)
        self.assertIn('WHERE t.id<=100',sql.call_args.args[0])
        for cutoff in (-1,True,'100; DELETE FROM management_audit'):
            with patch.object(chain,'sql') as sql,self.assertRaises(c.Blocked):chain.upload_probe_audit_rows(cutoff)
            sql.assert_not_called()

if __name__=='__main__':unittest.main()
