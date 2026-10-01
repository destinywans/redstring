import contextlib,copy,io,json,os
from pathlib import Path
import sys,tempfile,unittest,uuid
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_notification_routes_functional_coverage as routes

class NotificationRoutesTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);root=Path(self.temp.name).resolve()
        for file in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            f=root/file;f.parent.mkdir(parents=True,exist_ok=True);f.write_bytes((routes.ROOT/file).read_bytes())
        self.chain=routes.Chain(root,dict(owner=routes.OWNER,source_tree_sha=routes.SOURCE_SHA,binary_sha256=routes.BINARY_SHA,helper_sha256=routes.hashes(),postgres_container='own'),str(uuid.uuid4()))
        self.chain.apps={env:dict(key='owned-'+env,app=env+'-app',env_id=env+'-env') for env in ('test','staging')}
    def test_inventory_does_not_admit_or_start(self):
        with patch.object(routes,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(routes.main(['--inventory']),0)
        value=json.loads(out.getvalue());self.assertEqual(len(value['targets']),2);self.assertFalse(value['product_test_sends_notification']);self.assertEqual(value['ports'],[18648,18649,18650,18651,18652]);doctor.assert_not_called()
    def test_context_restores_every_shared_namespace_on_exception(self):
        old=routes.readonly.PORTS,routes.readonly.BASES,routes.readonly.emit,routes.readonly.transport
        with self.assertRaises(RuntimeError),routes.context():
            self.assertEqual(routes.readonly.PORTS,routes.PORTS);raise RuntimeError('controlled')
        self.assertEqual((routes.readonly.PORTS,routes.readonly.BASES,routes.readonly.emit,routes.readonly.transport),old)
    def test_bounded_contract_excludes_draft_write_and_external(self):
        for m,p in routes.TARGETS:self.assertEqual(self.chain.contract.canonical(m,'/api/v1'+p)[1],'main')
        for method,path in [('POST','/api/v1/notification-routes'),('GET','https://foreign/api/v1/notification-routes'),('POST','/api/v1/inhibitions/preview'),('GET','/notification-routes')]:
            with self.assertRaises(routes.Blocked):self.chain.contract.canonical(method,path)
    def test_owned_private_credentials_and_uuid_boundaries(self):
        self.assertIn('/.local/testlab/notification-routes-parallel/',str(self.chain.work));self.assertTrue(self.chain.database.endswith(self.chain.nonce));self.assertEqual((self.chain.work/'credentials.json').stat().st_mode&0o777,0o600)
    def test_config_all_receivers_only_exact_owned_capture(self):
        config=routes.config_for(self.chain);routes.validate_config(config,self.chain)
        self.assertEqual(config['route']['routes'][0]['matchers'],['environment="test"']);self.assertEqual(config['route']['routes'][1]['matchers'],['environment="staging"'])
        for mutate in [lambda c:c['receivers'][0]['webhook_configs'][0].update(url='https://external.invalid'),lambda c:c['receivers'][0].update(email_configs=[{}]),lambda c:c['route']['routes'][0].update(matchers=['environment=~".*"']),lambda c:c['receivers'][0]['webhook_configs'][0].update(http_config={'follow_redirects':True})]:
            value=copy.deepcopy(config);mutate(value)
            with self.assertRaises(AssertionError):routes.validate_config(value,self.chain)
    def info(self):
        return dict(Name='/'+self.chain.am_name,Id='c'*64,Image=routes.AM_IMAGE_ID,Path='/bin/alertmanager',Args=self.chain.am_args,
            State={'Running':True},Config={'User':str(os.getuid())+':'+str(os.getgid()),'Labels':{'aiops.testlab.owner':routes.OWNER,'aiops.testlab.run':self.chain.run_id,'aiops.testlab.purpose':'notification-routes'}},
            HostConfig={'NanoCpus':500000000,'Memory':268435456,'Privileged':False,'ReadonlyRootfs':True,'CapDrop':['ALL'],'SecurityOpt':['no-new-privileges'],'Tmpfs':{'/alertmanager':self.chain.tmpfs}},
            NetworkSettings={'Ports':{'9093/tcp':[{'HostIp':'127.0.0.1','HostPort':'18650'}]}},Mounts=[{'Type':'bind','Source':str(self.chain.work/'am-config.json'),'Destination':routes.AM_CONFIG,'RW':False}])
    def test_guard_rejects_owner_run_image_resource_mount_and_listener_drift(self):
        info=self.info();self.assertEqual(routes.guard_am(info,self.chain),'c'*64)
        for mutate in [lambda d:d.update(Image='sha256:'+'f'*64),lambda d:d['Config']['Labels'].update({'aiops.testlab.run':str(uuid.uuid4())}),lambda d:d['HostConfig'].update(Memory=1073741824),lambda d:d['HostConfig'].update(Privileged=True),lambda d:d['HostConfig'].update(CapDrop=[]),lambda d:d['Mounts'][0].update(RW=True),lambda d:d['NetworkSettings']['Ports']['9093/tcp'][0].update(HostIp='0.0.0.0'),lambda d:d['Config'].update(User='0:0')]:
            value=copy.deepcopy(info);mutate(value)
            with self.assertRaises(routes.Blocked):routes.guard_am(value,self.chain)
        self.chain.am_cid='a'*64
        with self.assertRaises(routes.Blocked):routes.guard_am(info,self.chain)
    def test_guard_stopped_only_same_exact_cid_port_binding(self):
        info=self.info();self.chain.am_cid=info['Id'];info['State']['Running']=False;info['HostConfig']['PortBindings']=info['NetworkSettings']['Ports'];info['NetworkSettings']['Ports']={}
        self.assertEqual(routes.guard_am(info,self.chain,live=False),'c'*64)
        with self.assertRaises(routes.Blocked):routes.guard_am(info,self.chain)
    def test_relay_scope_and_credential_dispatch_without_socket(self):
        source=routes.Source.__new__(routes.Source);source.chain=self.chain
        for env in ('test','staging'):self.assertEqual(source.validate_request('/'+env+'/api/v2/status','Bearer '+self.chain.am_tokens[env]),env)
        for path,token in [('/test/api/v2/status','Bearer '+self.chain.am_tokens['staging']),('/api/v2/status','Bearer '+self.chain.am_tokens['test']),('/test/api/v2/alerts','Bearer '+self.chain.am_tokens['test']),('https://external/test/api/v2/status','Bearer '+self.chain.am_tokens['test'])]:
            with self.assertRaises(routes.Blocked):source.validate_request(path,token)
    def test_no_am_arbitrary_write_before_connection(self):
        for method,path in [('GET','https://external/api/v2/status'),('DELETE','/api/v2/alerts'),('POST','/-/reload'),('GET','/api/v2/silences')]:
            with patch.object(routes.http.client,'HTTPConnection') as connection,self.assertRaises(routes.Blocked):routes.am_http(method,path)
            connection.assert_not_called()
    def test_exact_pg_cid_before_sql(self):
        with patch.object(routes.readonly,'inspect_pg',return_value={'Id':'foreign'}),patch.object(routes.readonly.Chain,'pg') as pg,self.assertRaises(routes.Blocked):self.chain.pg('SELECT 1')
        pg.assert_not_called()
    def test_normal_requires_validator_before_transport(self):
        with patch.object(self.chain,'frozen'),patch.object(routes.readonly.Chain,'call') as call,self.assertRaises(routes.Blocked):self.chain.call('reader','GET','/api/v1/notification-routes')
        call.assert_not_called()
    def path(self):
        return dict(environment_id='test',application_id='test-app',config_revision='sha256:'+'a'*64,observed_at='2026-10-01T00:00:00Z',partial=True,hits=None,channel_targets=None,unavailable=['receiver_names','hits','channel_targets','current_interval_activity'],paths=[dict(node_ids=['route-root','route-root-1'],receiver_refs=['receiver-2'],group_by=['application','environment','alertname'],group_wait='1s',group_interval='2s',repeat_interval='1h')])
    def test_sample_cannot_claim_delivery_hits_or_wrong_path(self):
        value=self.path();routes.verify_path(value,'test-app','test','sha256:'+'a'*64,'P1')
        for mutate in [lambda d:d.update(hits=1),lambda d:d.update(channel_targets=['real']),lambda d:d.update(environment_id='staging'),lambda d:d['paths'][0].update(receiver_refs=['receiver-3']),lambda d:d['paths'][0].update(group_wait='30s')]:
            value=self.path();mutate(value)
            with self.assertRaises(AssertionError):routes.verify_path(value,'test-app','test','sha256:'+'a'*64,'P1')
    def test_cleanup_attempts_both_listeners_even_if_pg_fails(self):
        self.chain.source=Mock()
        with patch.object(routes.readonly.Chain,'cleanup',side_effect=routes.Blocked('PG fault')),patch.object(routes.socket,'socket') as sock,self.assertRaises(routes.Blocked):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;self.chain.cleanup()
        self.chain.source.close.assert_called_once()
    def test_source_drift_blocks_before_runtime_reads(self):
        with patch.object(routes,'hashes',return_value={}),patch.object(routes.readonly.Chain,'frozen') as frozen,self.assertRaises(routes.Blocked):self.chain.frozen()
        frozen.assert_not_called()

if __name__=='__main__':unittest.main()
