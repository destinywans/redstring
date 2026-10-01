import contextlib,copy,hashlib,io,json,os,sys,tempfile,time,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_rule_assistant_container_coverage as container

class ContainerTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name).resolve()
        for name in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            p=self.root/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes((container.ROOT/name).read_bytes())
        image=[{'Id':container.IMAGE_ID,'Os':'linux','Architecture':'arm64','Config':{'Env':['PATH=/usr/local/bin:/usr/bin']}}]
        data={'owner':container.OWNER,'source_tree_sha':container.SOURCE_SHA,'binary_sha256':container.DARWIN_SHA,'postgres_container':'owned'}
        with patch.object(container.subprocess,'check_output',return_value=json.dumps(image)):
            self.c=container.Chain(self.root,data,str(uuid.uuid4()),{'binary_sha256':'b'*64,'binary':'/owned/linux','go_version':'go1.27.1'})
        self.c.network_id='a'*64
    def spec(self):
        return {'cid':'c'*64,'name':'owned-api','kind':'rule-assistant-api','memory':container.API_MEMORY,'cpu':1000000000,'mounts':[(Path('/private/owned/linux'),'/owned/aiops-linux')],'env':['PATH=/usr/local/bin:/usr/bin'],'aliases':[],'bindings':{'8080/tcp':[{'HostIp':'127.0.0.1','HostPort':'18690'}]},'sysctls':{},'path':'/owned/aiops-linux','args':[],'active':True,'index':0}
    def info(self,s):
        return {'Id':s['cid'],'Name':'/'+s['name'],'Image':container.IMAGE_ID,'Path':s['path'],'Args':s['args'],'State':{'Running':True,'ExitCode':0},'Config':{'User':str(container.UID)+':'+str(container.GID),'Labels':{'aiops.testlab.owner':container.OWNER,'aiops.testlab.run':self.c.run_id,'aiops.testlab.purpose':s['kind']},'Env':s['env'],'WorkingDir':'/owned'},'HostConfig':{'Memory':s['memory'],'NanoCpus':s['cpu'],'Privileged':False,'ReadonlyRootfs':True,'CapDrop':['ALL'],'SecurityOpt':['no-new-privileges'],'NetworkMode':self.c.network_name,'IpcMode':'private','RestartPolicy':{'Name':'no'},'AutoRemove':False,'Sysctls':None,'PortBindings':s['bindings']},'Mounts':[{'Source':str(p),'Destination':t,'Type':'bind','RW':False} for p,t in s['mounts']],'NetworkSettings':{'Networks':{self.c.network_name:{'NetworkID':self.c.network_id,'Aliases':[s['name'],s['cid'][:12],*s['aliases']]}},'Ports':s['bindings']}}
    def process(self,s):
        return {'uid':container.UID,'gid':container.GID,'proc1_uid':container.UID,'argv':[s['path'],*s['args']],'start_ticks':12345,'memory_max':s['memory'],'cpu_quota':1.0,'exe_sha256':self.c.build['binary_sha256']}
    def network(self):
        return {'Id':self.c.network_id,'Name':self.c.network_name,'Driver':'bridge','Scope':'local','Internal':False,'Ingress':False,'Attachable':False,'Options':dict(container.NETWORK_OPTIONS),'Labels':{'aiops.testlab.owner':container.OWNER,'aiops.testlab.run':self.c.run_id,'aiops.testlab.purpose':'rule-assistant-container'},'Containers':{}}
    def test_inventory_does_not_do_admission_or_docker(self):
        with patch.object(container.ro,'doctor') as doctor,patch.object(container.subprocess,'run') as run,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(container.main(['--root',str(self.root),'--inventory']),0)
        self.assertEqual(json.loads(out.getvalue())['evidence_mode'],'protocol_mock');doctor.assert_not_called();run.assert_not_called()
    def test_contract_only_two_primary_and_required_fixture_routes(self):
        for method,path in container.TARGETS:self.assertEqual(self.c.contract.canonical(method,'/api/v1'+path)[1],'main')
        for m,p in [('POST','/api/v1/metrics/ask'),('POST','/api/v1/alert-rules'),('POST','/api/v1/connectors'),('GET','/api/v1/services'),('POST','https://foreign/api/v1/alert-rules/generate')]:
            with self.assertRaises(container.ro.Blocked):self.c.contract.canonical(m,p)
    def test_catalog_retains_required_three_scope_labels_in_aggregate(self):
        app='a'*32;query=container.reviewed_expression(app,'test');self.assertEqual(query,'sum by (application,environment,service) (testlab_generation_rate{application="'+app+'",environment="test",service="owned-metrics"})')
        for scope in [('foreign','test'),(app,'production')]:
            with self.assertRaises(container.ro.Blocked):container.reviewed_expression(*scope)
    def test_scoped_context_restores_frozen_modules_after_error(self):
        before=(container.generation.PORTS,container.generation.BASES,container.generation.FIXTURE_PORT,container.generation.emit,container.generation.interface_evidence,container.ro.PORTS,container.ro.BASES,container.ro.emit,container.ro.append_json)
        with self.assertRaises(RuntimeError),container.context():raise RuntimeError('controlled')
        after=(container.generation.PORTS,container.generation.BASES,container.generation.FIXTURE_PORT,container.generation.emit,container.generation.interface_evidence,container.ro.PORTS,container.ro.BASES,container.ro.emit,container.ro.append_json)
        self.assertEqual(before,after)
    def test_case_nested_raw_and_actual_interface_keep_mock(self):
        for path in ['/api/v1'+p for _,p in container.TARGETS]:
            row={'method':'POST','path':path,'scenario':'normal','status':'passed','actual':{'status':200},'expected':{'status':[200]}}
            self.assertEqual(container.actual_interface(row)['evidence_mode'],'real_local')
            with contextlib.redirect_stdout(io.StringIO()) as out:container.emit('owned','passed',row['actual'],interface=row,evidence_mode='real_local')
            case=json.loads(out.getvalue());self.assertEqual(case['evidence_mode'],'protocol_mock');self.assertEqual(case['interface']['evidence_mode'],'protocol_mock');self.assertEqual(container.actual_interface(case['interface'])['evidence_mode'],'protocol_mock')
            with container.context():container.ro.append_json(self.c.work/'interfaces.jsonl',row)
        raw=[json.loads(x) for x in (self.c.work/'interfaces.jsonl').read_text().splitlines()];self.assertTrue(all(x['evidence_mode']==x['upstream_mode']=='protocol_mock' for x in raw))
    def test_headroom_reserves_pending_gitlab_and_fixed_memory(self):
        gib=1024**3;needed=container.NEW_MEMORY+gib+4*gib
        self.assertTrue(container.resource_admission(24*gib,needed,12,1)['passed'])
        for args in [(24*gib,needed-1,12,1),(24*gib,needed,6,1),(24*gib,25*gib,12,1)]:
            with self.assertRaises(container.ro.Blocked):container.resource_admission(*args)
    def test_active_gitlab_usage_not_reserved_twice(self):
        stats={'Container':'f'*64,'MemUsage':'2.775GiB / 4GiB'};info={'Id':'f'*64,'Name':'/aiops-'+container.OWNER+'-gitlab','Config':{'Labels':{'aiops.testlab.owner':container.OWNER}},'State':{'Running':True},'HostConfig':{'Memory':4*1024**3,'NanoCpus':2000000000}}
        group={'anon':2607423488,'memory_max':4*1024**3,'cpu_quota':2};pending,proof=container.gitlab_remaining_reserve(stats,info,group);self.assertLess(pending,2*1024**3);self.assertEqual(pending+proof['anonymous_resident_bytes'],4*1024**3);self.assertTrue(container.resource_admission(24*1024**3,5*1024**3,12,1,pending_memory=pending)['passed'])
        for change in [{'Id':'e'*64},{'Name':'/unrelated-gitlab'},{'Config':{'Labels':{}}},{'State':{'Running':False}}]:self.assertEqual(container.gitlab_remaining_reserve(stats,dict(info,**change),group)[0],4*1024**3)
        self.assertEqual(container.gitlab_remaining_reserve(stats,info)[0],4*1024**3)
        self.assertEqual(container.gitlab_remaining_reserve(stats,info,dict(group,anon=5*1024**3))[0],4*1024**3)
    def test_container_identity_security_and_mount_counterexamples(self):
        s=self.spec();info=self.info(s);self.assertEqual(container.guard_container(info,s,self.c),s['cid'])
        bad=[lambda x:x.update(Id='d'*64),lambda x:x.update(Image='other'),lambda x:x['Config']['Labels'].update({'aiops.testlab.run':str(uuid.uuid4())}),lambda x:x['Config'].update(User='0:0'),lambda x:x['HostConfig'].update(Memory=container.API_MEMORY*2),lambda x:x['HostConfig'].update(NanoCpus=2000000000),lambda x:x['HostConfig'].update(Privileged=True),lambda x:x['HostConfig'].update(ReadonlyRootfs=False),lambda x:x['HostConfig'].update(CapAdd=['NET_ADMIN']),lambda x:x['HostConfig'].update(PidMode='host'),lambda x:x['HostConfig'].update(IpcMode='host'),lambda x:x['HostConfig'].update(RestartPolicy={'Name':'always'}),lambda x:x['Mounts'].append({'Source':'/var/run/docker.sock','Destination':'/var/run/docker.sock','Type':'bind','RW':True}),lambda x:x['Mounts'][0].update(RW=True),lambda x:x['NetworkSettings']['Networks'].update(foreign={'NetworkID':'d'*64}),lambda x:x['NetworkSettings']['Networks'][self.c.network_name].update(NetworkID='d'*64),lambda x:x['NetworkSettings']['Networks'][self.c.network_name]['Aliases'].append('unreviewed.testlab.invalid'),lambda x:x['HostConfig']['PortBindings']['8080/tcp'][0].update(HostIp='0.0.0.0'),lambda x:x['Config']['Env'].append('OPENAI_BASE_URL=https://paid.example'),lambda x:x.update(Args=['foreign'])]
        for mutate in bad:
            x=copy.deepcopy(info);mutate(x)
            with self.assertRaises(container.ro.Blocked):container.guard_container(x,s,self.c)
    def test_actual_process_not_config_user_proves_uid_argv_elf_cgroup(self):
        s=self.spec();valid=self.process(s);self.assertEqual(container.guard_process(valid,s,self.c),valid)
        for change in [{'uid':0},{'proc1_uid':0},{'argv':['python']},{'exe_sha256':'f'*64},{'memory_max':2*container.API_MEMORY},{'cpu_quota':2},{'start_ticks':0}]:
            with self.assertRaises(container.ro.Blocked):container.guard_process(dict(valid,**change),s,self.c)
        s['process_identity']=valid
        with self.assertRaises(container.ro.Blocked):container.guard_process(dict(valid,start_ticks=12346),s,self.c)
    def test_created_endpoint_is_separate_from_runtime_network_proof(self):
        s=self.spec();info=self.info(s);info['State'].update(Status='created',Pid=0,Running=False);info['NetworkSettings']['Networks'][self.c.network_name]['NetworkID']=''
        self.assertEqual(container.guard_container(info,s,self.c,live=False),s['cid'])
        with self.assertRaises(container.ro.Blocked):container.guard_container(info,s,self.c)
        info['State'].update(Status='running',Running=True)
        with self.assertRaises(container.ro.Blocked):container.guard_container(info,s,self.c)
    def test_private_network_id_and_foreign_members_are_rejected(self):
        self.assertEqual(container.guard_network(self.network(),self.c),self.c.network_id)
        for change in [{'Id':'f'*64},{'Driver':'host'},{'Containers':{'foreign':{}}},{'Options':{'bridge.name':'docker0'}}]:
            with self.assertRaises(container.ro.Blocked):container.guard_network(dict(self.network(),**change),self.c)
    def test_stop_identity_drift_never_calls_docker_stop_or_rm(self):
        s=self.spec();info=self.info(s);info['Id']='f'*64
        with patch.object(self.c,'inspect_container',return_value=info),patch.object(self.c,'docker') as docker,self.assertRaises(container.ro.Blocked):self.c.stop_container(s)
        docker.assert_not_called()
    def test_unknown_create_without_fresh_cid_does_not_adopt_name(self):
        s=self.spec();s['cid']=None
        with patch.object(self.c,'docker',return_value=Mock(returncode=0)) as docker,self.assertRaises(container.ro.Blocked):self.c.stop_container(s)
        self.assertEqual(docker.call_count,1);self.assertEqual(docker.call_args.args[0],['inspect',s['name']])
    def test_exact_pg_tuple_rejects_pid_reuse_wrong_role_address_port(self):
        row={'pid':20,'backend_start':'2026-10-01 12:00:00Z','datname':self.c.database,'usename':self.c.db_user,'application_name':self.c.pg_application('api',0),'client_addr':'192.0.2.1','client_port':3456,'backend_type':'client backend'};self.c.attested_pg_sessions.add(self.c.pg_tuple(row));self.assertEqual(self.c.connection_origin(dict(row,state='idle')),'owned_api')
        for changed in [{'backend_start':'2026-10-01 13:00:00Z'},{'usename':'foreign'},{'datname':'canonical'},{'client_addr':'192.0.2.2'},{'client_port':3457},{'backend_type':'autovacuum worker'}]:
            with self.assertRaises(container.ro.Blocked):self.c.connection_origin(dict(row,**changed))
    def test_capture_pg_uses_host_address_matching_parent_cleanup(self):
        row={'pid':20,'backend_start':'2026-10-01 12:00:00Z','datname':self.c.database,'usename':self.c.db_user,'application_name':self.c.pg_application('api',0),'client_addr':'127.0.0.1','client_port':3456,'backend_type':'client backend'}
        with patch.object(self.c,'sql',return_value=json.dumps([row])) as sql:self.c.capture_pg()
        self.assertIn("'client_addr',host(client_addr)",sql.call_args.args[0]);self.assertNotIn('client_addr::text',sql.call_args.args[0]);self.assertEqual(self.c.connection_origin(row),'owned_api')
        with patch.object(self.c,'sql',return_value=json.dumps([dict(row,client_addr='127.0.0.1/32')])),self.assertRaises(ValueError):self.c.capture_pg()
    def test_cleanup_deadline_rejects_before_sql_or_docker(self):
        self.c.cleanup_deadline=time.monotonic()-1
        with patch.object(container.ro,'inspect_pg') as inspect,patch.object(container.ro.Chain,'pg') as pg,self.assertRaises(container.ro.Blocked):self.c.pg('SELECT 1')
        inspect.assert_not_called();pg.assert_not_called()
        with patch.object(container.subprocess,'run') as run,self.assertRaises(container.ro.Blocked):self.c.docker(['stop','unknown'])
        run.assert_not_called()
        with patch.object(container.subprocess,'run') as run,self.assertRaises(container.ro.Blocked):self.c.stop_container(self.spec())
        run.assert_not_called()
    def test_cleanup_guard_failure_retains_pg_and_avoids_drop(self):
        s=self.spec();self.c.containers=[s];self.c.db_attempted=True;self.c.network_id=None
        with patch.object(self.c,'stop_container',side_effect=container.ro.Blocked('CID drift')),patch.object(container.ro.Chain,'cleanup') as cleanup,self.assertRaises(container.ro.Blocked):self.c.cleanup()
        cleanup.assert_not_called();r=json.loads((self.c.work/'container-cleanup.json').read_text());self.assertFalse(r['pg_cleanup_eligible']);self.assertIn('CID drift',r['errors'])
    def test_pg_read_failure_still_closes_owned_containers_and_fixture(self):
        s=self.spec();self.c.containers=[s];self.c.api=[s];self.c.db_attempted=True;self.c.network_id=None;self.c.rule_fixture=Mock()
        def stopped(spec):spec['removed']=True
        with patch.object(self.c,'capture_pg',side_effect=container.ro.Blocked('PG read unavailable')),patch.object(self.c,'stop_container',side_effect=stopped) as stop,patch.object(container.ro.Chain,'cleanup') as cleanup,self.assertRaises(container.ro.Blocked):self.c.cleanup()
        stop.assert_called_once_with(s);self.c.rule_fixture.close.assert_called_once();cleanup.assert_not_called()
    def test_uuid_private_records_and_no_real_seed(self):
        self.assertIn('/rule-assistant-container/',str(self.c.work));self.assertTrue(self.c.database.endswith(self.c.nonce));self.assertEqual((self.c.work/'credentials.json').stat().st_mode&0o777,0o600)
    def test_provision_uses_valid_select_and_only_schema_identity_seed(self):
        self.c.network_id=None;queries=[]
        def pg(sql,*args,**kwargs):queries.append(sql);return '{"role":false,"database":false}' if sql.startswith('SELECT') else ''
        with patch.object(self.c,'frozen'),patch.object(self.c,'pg',side_effect=pg),patch.object(self.c,'marker_case'):self.c.provision()
        self.assertTrue(queries[0].startswith('SELECT json_build_object'));self.assertNotIn('SELECT1',queries[0]);self.assertTrue(all('INSERT INTO' not in q for q in queries));self.assertTrue(all('canonical' not in q for q in queries))

class PreparedBuildTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name).resolve()
        self.base=self.root/'.local/testlab/rule-assistant-container/build';self.base.mkdir(mode=0o700,parents=True);self.index=self.base/'current.json'
        work=self.base/str(uuid.uuid4());work.mkdir(mode=0o700);self.proof=work/'build-proof.json';self.binary=work/'aiops-linux-arm64'
        elf=bytearray(64);elf[:6]=b'\x7fELF\x02\x01';elf[18:20]=(183).to_bytes(2,'little');self.binary.write_bytes(elf)
        self.data={'binary':str(self.binary),'binary_sha256':hashlib.sha256(elf).hexdigest(),'source_tree_sha_start':container.SOURCE_SHA,'source_tree_sha_end':container.SOURCE_SHA,'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0','GOTOOLCHAIN':'local','flags':['-trimpath'],'package':'./cmd/aiops','exit_code':0,'build_info':'GOOS=linux GOARCH=arm64 CGO_ENABLED=0'}
        self.write(self.proof,self.data);self.patch=patch.object(container,'ROOT',self.root);self.patch.start();self.addCleanup(self.patch.stop)
    def write(self,path,data):path.write_text(json.dumps(data));path.chmod(0o600)
    def test_missing_or_wrong_schema_pointer_is_blocked_before_admission(self):
        for data in [None,{},[],{'proof':str(self.proof),'latest':True},{'proof':7},{'proof':'relative/build-proof.json'}]:
            with self.subTest(pointer=data):
                if data is not None:self.write(self.index,data)
                with patch.object(container.ro,'doctor') as doctor,patch.object(container,'lease') as lease,contextlib.redirect_stdout(io.StringIO()) as out:
                    self.assertEqual(container.main(['--root',str(self.root),'--live']),2)
                self.assertEqual(json.loads(out.getvalue())['status'],'blocked');doctor.assert_not_called();lease.assert_not_called()
    def test_symlink_pointer_proof_and_parent_are_rejected(self):
        pointer=self.base/'pointer.json';self.write(pointer,{'proof':str(self.proof)});self.index.symlink_to(pointer)
        with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root)
        self.index.unlink();link=self.proof.parent/'linked';link.symlink_to(self.proof.parent,target_is_directory=True)
        self.write(self.index,{'proof':str(link/'build-proof.json')})
        with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root)
        saved=self.proof.parent/'saved.json';self.proof.rename(saved);self.proof.symlink_to(saved);self.write(self.index,{'proof':str(self.proof)})
        with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root)
    def test_wrong_proof_or_override_never_falls_back(self):
        self.write(self.index,{'proof':str(self.proof)})
        with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root,self.base/'absent/build-proof.json')
        for change in [{'source_tree_sha_end':'f'*64},{'binary_sha256':'f'*64},{'GOOS':'darwin'},{'build_info':'GOOS=linux GOARCH=arm64 CGO_ENABLED=0 -race=true'}, {'binary':str(self.binary.parent/'missing')}]:
            with self.subTest(change=change):
                self.write(self.proof,dict(self.data,**change))
                with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root)
                with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root,self.proof)
        self.proof.write_text('broken JSON');self.proof.chmod(0o600)
        with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root)
    def test_explicit_override_precedes_index_and_still_validates_binary(self):
        self.assertEqual(container.prepared_linux_build(self.root,self.proof),self.data)
        self.write(self.index,{'wrong':'schema'})
        self.assertEqual(container.prepared_linux_build(self.root,self.proof),self.data)
        self.write(self.index,{'proof':str(self.proof)})
        self.assertEqual(container.prepared_linux_build(self.root),self.data)
        saved=self.binary.parent/'saved-binary';self.binary.rename(saved);self.binary.symlink_to(saved)
        with self.assertRaises(container.ro.Blocked):container.prepared_linux_build(self.root,self.proof)

if __name__=='__main__':unittest.main()
