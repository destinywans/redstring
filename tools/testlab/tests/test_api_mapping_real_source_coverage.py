import contextlib, copy, io, json, sys, unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_mapping_real_source_coverage as mapping

RUN='39224b91-ae6a-4773-9b42-fdd25a1dca3c'

class MappingRealTests(unittest.TestCase):
    def candidate(self):
        return dict(id='candidate',version='2026-10-05T12:00:00Z',expires_at='2026-10-05T12:15:00Z',
            service_id='service',environment_id='test',system_id='code',connector_id='connector',
            target_id='target',external_id='1',binding_revision=1)

    def token(self):
        return dict(id=41,name='mapping-real-'+RUN,scopes=['read_api'],access_level=20,
            expires_at='2026-10-06',revoked=False,token='private-synthetic-token')

    def info(self,kind):
        return dict(Id=mapping.GITLAB_CID if kind=='gitlab' else 'a'*64,
            Image=mapping.GITLAB_IMAGE_ID if kind=='gitlab' else 'sha256:'+'b'*64,
            Name='/aiops-'+mapping.OWNER+'-'+kind,
            Config=dict(Image=mapping.GITLAB_IMAGE if kind=='gitlab' else 'hashicorp/vault:1.20.4',
                Labels={'aiops.testlab.owner':mapping.OWNER}),State=dict(Running=True,StartedAt='fixed'),
            NetworkSettings=dict(Ports={('443/tcp' if kind=='gitlab' else '8200/tcp'):
                [dict(HostIp='127.0.0.1',HostPort='18580' if kind=='gitlab' else '18572')]}))

    def chain(self):
        chain=mapping.Chain.__new__(mapping.Chain)
        chain.run_id=RUN; chain.token_id=41; chain.token_name='mapping-real-'+RUN
        chain.secret='private-synthetic-token'; chain.vault_token='private-synthetic-vault'
        chain.token_unknown=False; chain.vault_unknown=False
        chain.policy_pending=True; chain.secret_pending=True
        chain.policy='mapping-real-'+RUN; chain.vault_path='testlab-mapping-real/'+RUN
        chain.work=mapping.ROOT/'.local/testlab/nonexecuted-mapping-tests'
        return chain

    def test_private_helper_and_exact_formal_inventory(self):
        self.assertIsNot(mapping.ro,shared); self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(mapping.PORTS,(18754,18755))
        contract=mapping.Contract(mapping.ROOT)
        for method,path in (*mapping.TARGETS,*mapping.SUPPORT):contract.canonical(method,'/api/v1'+path)
        for method,path in [('POST','/api/v1/incidents'),('POST','/api/v1/service-mappings/discover?env=test'),
                            ('POST','/api/v1/models/test'),('GET','https://elsewhere/service-mappings')]:
            with self.assertRaises((mapping.Blocked,AssertionError)):contract.canonical(method,path)

    def test_business_gitlab_only_exact_get_project_and_user(self):
        for path in ('/api/v4/user','/api/v4/projects/1'):
            self.assertTrue(mapping.remote_allowed('gitlab','GET',path,RUN))
        for method,path in [('POST','/api/v4/projects/1/pipeline'),('GET','/api/v4/projects/2'),
                            ('GET','/api/v4/projects/1/repository/branches'),('GET','/api/v4/projects/1?x=y'),
                            ('DELETE','/api/v4/projects/1/access_tokens/41')]:
            self.assertFalse(mapping.remote_allowed('gitlab',method,path,RUN,41))

    def test_provisioner_no_root_business_read_or_name_search(self):
        self.assertTrue(mapping.remote_allowed('gitlab','POST','/api/v4/projects/1/access_tokens',RUN,provisioner=True))
        for method in ('GET','DELETE'):
            self.assertTrue(mapping.remote_allowed('gitlab',method,'/api/v4/projects/1/access_tokens/41',RUN,41,True))
        for method,path in [('GET','/api/v4/projects/1'),('GET','/api/v4/projects/1/access_tokens'),
                            ('GET','/api/v4/projects/2/access_tokens/41'),('DELETE','/api/v4/projects/1/access_tokens/42')]:
            self.assertFalse(mapping.remote_allowed('gitlab',method,path,RUN,41,True))

    def test_vault_uuid_path_guards(self):
        path='/v1/secret/data/testlab-mapping-real/'+RUN
        self.assertTrue(mapping.remote_allowed('vault','GET',path,RUN))
        self.assertTrue(mapping.remote_allowed('vault','GET',path+'-sibling',RUN))
        for method,bad in [('GET','/v1/secret/data/shared'),('DELETE',path),('GET','/v1/auth/token/create'),
                           ('PUT','/v1/sys/policies/acl/mapping-real-another')]:
            self.assertFalse(mapping.remote_allowed('vault',method,bad,RUN))

    def test_owned_dependency_exact_cid_and_ports(self):
        info=self.info('gitlab'); identity=mapping.guard_remote(info,'gitlab')
        self.assertEqual(identity['container_id'],mapping.GITLAB_CID)
        for mutation in ('cid','label','port','image','running'):
            changed=copy.deepcopy(info)
            if mutation=='cid':changed['Id']='c'*64
            elif mutation=='label':changed['Config']['Labels']['aiops.testlab.owner']='other'
            elif mutation=='port':changed['NetworkSettings']['Ports']['443/tcp'][0]['HostIp']='0.0.0.0'
            elif mutation=='image':changed['Image']='sha256:'+'d'*64
            else:changed['State']['Running']=False
            with self.assertRaises(AssertionError):mapping.guard_remote(changed,'gitlab')
        changed=copy.deepcopy(info);changed['State']['StartedAt']='restart'
        with self.assertRaises(AssertionError):mapping.guard_remote(changed,'gitlab',identity)

    def test_vault_identity_pins_restart(self):
        info=self.info('vault'); pinned=mapping.guard_remote(info,'vault')
        info['State']['StartedAt']='restart'
        with self.assertRaises(AssertionError):mapping.guard_remote(info,'vault',pinned)

    def test_project_reporter_read_api_token_contract(self):
        value=self.token()
        self.assertEqual(mapping.validate_project_token(value,value['name'],value['expires_at']),(41,value['token']))
        for field,replacement in [('scopes',['api']),('access_level',40),('name','another-run'),('revoked',True),
                                  ('id','41'),('expires_at','2099-01-01'),('token','bad\nheader')]:
            changed=copy.deepcopy(value); changed[field]=replacement
            with self.assertRaises(AssertionError):mapping.validate_project_token(changed,value['name'],value['expires_at'])

    def test_discovery_rejects_empty_protocol_health_as_normal(self):
        with self.assertRaises(AssertionError):mapping.validate_discovery(dict(source_state='not_configured',suggestions=0,candidates=[]),{})
        row=self.candidate(); expected={k:row[k] for k in ('service_id','environment_id','system_id','connector_id','target_id','external_id','binding_revision')}
        value=dict(source_state='configured',suggestions=1,candidates=[row])
        mapping.validate_discovery(value,expected)
        row['external_id']='101'
        with self.assertRaises(AssertionError):mapping.validate_discovery(value,expected)

    def test_accept_and_replay_exact_native_linkage(self):
        row=self.candidate()
        value=dict(accepted=1,items=[dict(candidate_id='candidate',service_id='service',environment_id='test',
            system_id='code',connector_id='connector',target_id='target',external_id='1',binding_revision=2,
            binding=dict(kind='gitlab',connector_id='connector',resource='1'),status='accepted')])
        mapping.validate_accept(value,row,'connector')
        value['accepted']=0;value['items'][0]['status']='already_accepted'
        mapping.validate_accept(value,row,'connector',True)
        value['items'][0]['binding_revision']=3
        with self.assertRaises(AssertionError):mapping.validate_accept(value,row,'connector',True)

    def test_readback_counterexample_substring_false_positive(self):
        key='service-real';app=dict(id='app',env_id='env',revision=1)
        old_weak=dict(rows=[dict(service=key,cells=dict(code=dict(state='ok',value='101',brand='gitlab')))],suggestions=0)
        self.assertIn(key,json.dumps(old_weak)); self.assertIn('1',json.dumps(old_weak))
        with self.assertRaises(AssertionError):mapping.validate_readback(old_weak,key,app,'connector')
        correct=dict(rows=[dict(service=key,cells=dict(code=dict(state='ok',value='1',brand='gitlab')),
            save=dict(service_id='app',service_environment_id='env',binding_revision=2,editable=True,
                bindings=[dict(system_id='code',kind='gitlab',connector_id='connector',resource='1')]))],suggestions=0)
        mapping.validate_readback(correct,key,app,'connector')

    def test_accept_body_selects_original_six_fields_only(self):
        result=mapping.accept_body([self.candidate()])['candidates'][0]
        self.assertEqual(set(result),{'id','version','service_id','environment_id','system_id','binding_revision'})

    def test_explicit_live_required_before_resource_acquisition(self):
        with mock.patch.object(mapping,'doctor') as doctor, mock.patch.object(mapping.os,'open') as acquire:
            with self.assertRaises(SystemExit),contextlib.redirect_stderr(io.StringIO()):mapping.main([])
            doctor.assert_not_called();acquire.assert_not_called()

    def test_cleanup_external_uses_one_shared_deadline(self):
        chain=self.chain();calls=[]
        def remote(kind,method,path,*args,**kwargs):
            calls.append((kind,method,path,kwargs['deadline']))
            return self.token() if method=='GET' and '/access_tokens/' in path else {}
        chain.remote=remote
        with mock.patch.object(mapping.time,'monotonic',return_value=10),mock.patch.object(mapping.ro,'_private'):
            chain.cleanup_external()
        self.assertEqual(len(calls),6);self.assertEqual({c[3] for c in calls},{14})
        self.assertEqual(calls[1][1],'DELETE');self.assertEqual(calls[2][2],'/api/v4/user')

    def test_restore_failure_still_runs_original_cleanup(self):
        chain=self.chain()
        error=mapping.Blocked('first cleanup fault')
        with mock.patch.object(chain,'cleanup_external',side_effect=error),mock.patch.object(mapping.ro.Chain,'cleanup') as original:
            with self.assertRaises(mapping.Blocked) as raised:chain.cleanup()
            self.assertIs(raised.exception,error);original.assert_called_once()

    def test_unknown_token_create_never_name_search_or_adoption(self):
        chain=self.chain();chain.token_id=None;chain.token_unknown=True
        chain.vault_token='';chain.policy_pending=False;chain.secret_pending=False
        chain.remote=mock.Mock()
        with mock.patch.object(mapping.ro,'_private'):
            with self.assertRaises(AssertionError):chain.cleanup_external()
        chain.remote.assert_not_called()

    def test_remote_identity_failure_precedes_credential_send(self):
        chain=self.chain();chain.data=dict(remote_identities={'gitlab':{}},root_token='synthetic')
        with mock.patch.object(mapping,'_inspect',return_value={}),mock.patch.object(mapping.http.client,'HTTPSConnection') as connection:
            with self.assertRaises(AssertionError):chain.remote('gitlab','GET','/api/v4/user')
            connection.assert_not_called()

    def test_exhausted_cleanup_does_not_call_docker_or_http(self):
        chain=self.chain();chain.data=dict(remote_identities={'gitlab':{}})
        with mock.patch.object(mapping.time,'monotonic',return_value=20),mock.patch.object(mapping,'_inspect') as inspect:
            with self.assertRaises(mapping.Blocked):chain.remote('gitlab','GET','/api/v4/user',deadline=14)
            inspect.assert_not_called()

    def test_cleanup_ignores_source_drift_but_keeps_remote_identity(self):
        chain=self.chain();chain.token_id=None;chain.vault_token='';chain.policy_pending=False;chain.secret_pending=False
        with (mock.patch.object(chain,'frozen',side_effect=mapping.Blocked('source changed')) as frozen,
              mock.patch.object(mapping.ro,'_private'),mock.patch.object(mapping.ro.Chain,'cleanup') as original,
              mock.patch.object(mapping,'emit')):
            chain.cleanup()
            frozen.assert_not_called();original.assert_called_once()

    def test_unknown_provision_is_journaled_before_request(self):
        chain=self.chain(); chain.expiry='2026-10-06'; chain.token_id=None
        events=[]
        def private(path,value): events.append(('journal',path.name))
        def remote(*args,**kwargs):
            events.append(('request',args[2])); raise mapping.Blocked('outcome unknown')
        chain.remote=remote
        with mock.patch.object(mapping.ro,'_private',side_effect=private):
            with self.assertRaises(mapping.Blocked):chain.prepare_sources()
        self.assertEqual(events,[('journal','gitlab-token-intent.json'),('request','/api/v4/projects/1/access_tokens')])
        self.assertTrue(chain.token_unknown);self.assertIsNone(chain.token_id)

    def test_wrong_cleanup_token_identity_never_deletes(self):
        chain=self.chain();chain.vault_token='';chain.policy_pending=False;chain.secret_pending=False
        changed=self.token(); changed['name']='someone-elses-token'
        chain.remote=mock.Mock(return_value=changed)
        with mock.patch.object(mapping.ro,'_private'):
            with self.assertRaises(AssertionError):chain.cleanup_external()
        chain.remote.assert_called_once()
        self.assertEqual(chain.remote.call_args.args[1],'GET')

    def test_unknown_vault_create_is_not_hidden_by_other_cleanup(self):
        chain=self.chain();chain.token_id=None;chain.vault_token='';chain.vault_unknown=True
        chain.policy_pending=False;chain.secret_pending=False;chain.remote=mock.Mock()
        with mock.patch.object(mapping.ro,'_private') as receipt:
            with self.assertRaises(AssertionError):chain.cleanup_external()
            self.assertTrue(receipt.call_args.args[1]['vault_creation_unknown'])
        chain.remote.assert_not_called()

if __name__=='__main__':unittest.main()
