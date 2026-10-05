import base64, contextlib, copy, datetime, io, json, sys, unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_service_discovery_real_source_coverage as discovery

RUN='9cddab48-9f8c-4809-93ba-b1c779c05845'
UID='3cde8a75-5dd0-4ce7-b8a5-aa62449721ed'
SOURCE_ID='discovery-kube-fixture'

class DiscoveryRealTests(unittest.TestCase):
    def info(self):
        return dict(Id=discovery.KIND_CID,Image=discovery.KIND_IMAGE,Name='/'+discovery.CLUSTER+'-control-plane',
            State=dict(Running=True,StartedAt='fixed'),Config=dict(Labels={'aiops.testlab.owner':discovery.OWNER,
                'io.x-k8s.kind.cluster':discovery.CLUSTER,'io.x-k8s.kind.role':'control-plane'}),
            NetworkSettings=dict(Ports={'6443/tcp':[dict(HostIp='127.0.0.1',HostPort='55840')]}))

    def workload(self,kind='Deployment'):
        value=dict(kind=kind,metadata=dict(name='coredns',namespace='kube-system',uid=UID,
            labels={'team':' unverified-team ','tier':' T1 '}),spec=dict(template=dict(spec=dict(containers=[dict(image='real:tag')]))))
        if kind=='DaemonSet':value['status']=dict(desiredNumberScheduled=3)
        else:value['spec']['replicas']=2
        return value

    def dto(self,workload):
        stamp=datetime.datetime.now(datetime.timezone.utc).isoformat()
        item=dict(id=workload['metadata']['name'],namespace=workload['metadata']['namespace'],uid=UID,
            kind=workload['kind'],environment_id='test',cluster=discovery.CLUSTER,replicas=discovery.projected_replicas(workload),
            image='real:tag',resource_id=discovery.resource_id(SOURCE_ID,UID),owner_hint='Unknown',
            owner_label_hint='team=unverified-team',tier_hint='T1',matched=[],missing=[],
            source=dict(kind='kubernetes',id=SOURCE_ID,observed_at=stamp,last_success_at=stamp),
            unavailable_fields=['health','verified_tier','connector_coverage','runbook_gaps','verified_owner'])
        return dict(cluster=discovery.CLUSTER,registered=0,ambiguous_workloads=0,items=[item])

    def chain(self):
        chain=discovery.Chain.__new__(discovery.Chain)
        chain.run_id=RUN;chain.nonce=RUN.replace('-','');chain.auth=[];chain.unknown=[];chain.read_token='synthetic-never-used'
        chain.work=discovery.ROOT/'.local/testlab/nonexecuted-discovery-unit-tests'
        chain.source_id=SOURCE_ID;chain.private_hashes={};chain.expected={}
        chain.data=dict(kind_identity=discovery.guard_kind(self.info()),kubeconfig_sha256='hash',
            namespace_uid='6415bf47-ccaa-4b4a-b226-cedb730c23b0',receipt=dict(deployments=[dict(name='coredns',namespace='kube-system',uid=UID)]))
        chain.tls_admin=chain.tls_reader=object()
        return chain

    def records(self):
        return [dict(path=collection+'/'+spec['metadata']['name'],spec=spec,uid=UID)
            for collection,spec in discovery.auth_specs(RUN)]

    def test_private_helper_and_only_discovery_inventory(self):
        self.assertIsNot(discovery.ro,shared);self.assertEqual(shared.PORTS,(18602,18603))
        self.assertEqual(discovery.PORTS,(18774,18775));self.assertEqual(len(discovery.ENDPOINTS),15)
        contract=discovery.Contract(discovery.ROOT)
        for method,path in (*discovery.TARGETS,*discovery.SUPPORT):contract.canonical(method,'/api/v1'+path)
        contract.canonical('GET','/api/v1/services/discover?cluster=a&cluster=b')
        for method,path in [('POST','/api/v1/services'),('GET','/api/v1/resources'),('POST','/api/v1/models/test'),
                            ('GET','https://elsewhere/api/v1/services/discover'),('POST','/api/v1/auth/login?x=y')]:
            with self.assertRaises((AssertionError,discovery.Blocked)):contract.canonical(method,path)

    def test_all_runtime_helpers_and_test_source_are_frozen(self):
        values=discovery.hashes()
        self.assertIn('tests/test_api_service_discovery_real_source_coverage.py',values)
        self.assertIn('store.py',values);self.assertTrue(all(len(x)==64 for x in values.values()))

    def test_exact_owned_kind_cid_image_listener_and_restart(self):
        value=self.info();identity=discovery.guard_kind(value)
        for field in ('cid','image','owner','cluster','role','listener','running','restart'):
            changed=copy.deepcopy(value)
            if field=='cid':changed['Id']='a'*64
            elif field=='image':changed['Image']='sha256:'+'b'*64
            elif field=='owner':changed['Config']['Labels']['aiops.testlab.owner']='other'
            elif field=='cluster':changed['Config']['Labels']['io.x-k8s.kind.cluster']='other'
            elif field=='role':changed['Config']['Labels']['io.x-k8s.kind.role']='worker'
            elif field=='listener':changed['NetworkSettings']['Ports']['6443/tcp'][0]['HostIp']='0.0.0.0'
            elif field=='running':changed['State']['Running']=False
            else:changed['State']['StartedAt']='restarted'
            with self.assertRaises(AssertionError):discovery.guard_kind(changed,identity)

    def test_auth_scope_exact_15_readonly_kinds_no_namespace_or_workload_writes(self):
        specs=discovery.auth_specs(RUN)
        self.assertEqual([v['kind'] for _,v in specs],['ServiceAccount','ClusterRole','ClusterRoleBinding'])
        self.assertIs(specs[0][1]['automountServiceAccountToken'],False)
        self.assertEqual(sum(len(r['resources']) for r in specs[1][1]['rules']),15)
        for rule in specs[1][1]['rules']:
            self.assertEqual(rule['verbs'],['get','list','watch'])
            self.assertFalse(set(rule['resources'])&{'secrets','configmaps','pods/exec','*'})
        self.assertEqual(specs[2][1]['subjects'],[dict(kind='ServiceAccount',name='discovery-'+RUN.replace('-',''),namespace=discovery.NAMESPACE)])

    def test_reader_http_only_15_list_paths_and_denied_ssar(self):
        for path,_ in discovery.ENDPOINTS:
            self.assertTrue(discovery.kube_allowed('GET',path+'?limit=500',False,RUN))
        self.assertTrue(discovery.kube_allowed('POST','/apis/authorization.k8s.io/v1/selfsubjectaccessreviews',False,RUN))
        for method,path in [('GET','/api/v1/secrets'),('GET','/api/v1/configmaps'),('POST','/api/v1/pods'),
                            ('GET','/api/v1/namespaces?watch=true'),('GET','/api/v1/pods?limit=500&limit=1'),
                            ('GET','/api/v1/pods?continue=unreviewed'),('GET','https://foreign/api/v1/pods')]:
            self.assertFalse(discovery.kube_allowed(method,path,False,RUN))

    def test_admin_only_exact_auth_uuid_and_namespace_get(self):
        for collection,spec in discovery.auth_specs(RUN):
            item=collection+'/'+spec['metadata']['name']
            for method,path in [('POST',collection),('GET',item),('DELETE',item)]:
                self.assertTrue(discovery.kube_allowed(method,path,True,RUN))
            self.assertFalse(discovery.kube_allowed('DELETE',item+'-other',True,RUN))
            self.assertFalse(discovery.kube_allowed('GET',collection,True,RUN))
        for method,path in [('POST','/api/v1/namespaces'),('DELETE','/api/v1/namespaces/'+discovery.NAMESPACE),
                            ('GET','/apis/apps/v1/deployments'),('PATCH','/apis/apps/v1/deployments/coredns')]:
            self.assertFalse(discovery.kube_allowed(method,path,True,RUN))

    def test_cleanup_identity_refuses_uid_owner_rules_and_binding_drift(self):
        for record in self.records():
            value=copy.deepcopy(record['spec']);value['metadata']['uid']=UID
            discovery.owned_auth(value,record['spec'],UID)
            for field in ('uid','owner','run','spec'):
                changed=copy.deepcopy(value)
                if field=='uid':changed['metadata']['uid']='a'*36
                elif field=='owner':changed['metadata']['labels']['aiops.testlab.owner']='other'
                elif field=='run':changed['metadata']['labels']['aiops.testlab.run']='other'
                elif value['kind']=='ClusterRole':changed['rules'][0]['verbs'].append('create')
                elif value['kind']=='ClusterRoleBinding':changed['subjects'][0]['name']='other'
                else:changed['automountServiceAccountToken']=True
                with self.assertRaises(AssertionError):discovery.owned_auth(changed,record['spec'],UID)

    def test_actual_daemonset_counterexample_old_spec_replicas_rejects_formal_projection(self):
        original=self.workload('DaemonSet');value=self.dto(original)
        self.assertIsNone(original['spec'].get('replicas'))
        self.assertNotEqual(value['items'][0]['replicas'],original['spec'].get('replicas'))
        discovery.validate_discovery(value,{UID:original},SOURCE_ID)
        broken=copy.deepcopy(value);broken['items'][0]['replicas']=None
        broken['items'][0]['unavailable_fields'].append('desired_replicas')
        with self.assertRaises(AssertionError):discovery.validate_discovery(broken,{UID:original},SOURCE_ID)

    def test_deployment_stateful_and_daemon_defaults_follow_formal_convert(self):
        for kind,expected in [('Deployment',1),('StatefulSet',1),('DaemonSet',0)]:
            original=self.workload(kind);original['spec'].pop('replicas',None);original.pop('status',None)
            self.assertEqual(discovery.projected_replicas(original),expected)
            value=self.dto(original);discovery.validate_discovery(value,{UID:original},SOURCE_ID)
            self.assertNotIn('desired_replicas',value['items'][0]['unavailable_fields'])

    def test_source_uid_resource_id_field_projection_and_no_fabricated_owner(self):
        original=self.workload();value=self.dto(original)
        discovery.validate_discovery(value,{UID:original},SOURCE_ID)
        for key,new in [('resource_id','mock'),('uid','other'),('namespace','staging'),('kind','Mock'),
                        ('environment_id','staging'),('image','fabricated'),('replicas',9),('owner_hint','unverified-team'),
                        ('tier_hint',' T1 '),('matched',['connector']),('missing',['guess']),('owner_label_hint','team=foreign')]:
            changed=copy.deepcopy(value);changed['items'][0][key]=new
            with self.assertRaises(AssertionError):discovery.validate_discovery(changed,{UID:original},SOURCE_ID)

    def test_stale_or_protocol_source_not_real_normal(self):
        original=self.workload();value=self.dto(original)
        for field,new in [('kind','protocol_mock'),('id','mock'),('observed_at','2000-01-01T00:00:00Z'),
                          ('last_success_at','2099-01-01T00:00:00Z')]:
            changed=copy.deepcopy(value);changed['items'][0]['source'][field]=new
            with self.assertRaises(AssertionError):discovery.validate_discovery(changed,{UID:original},SOURCE_ID)

    def test_duplicate_uids_or_empty_cannot_claim_normal(self):
        original=self.workload();value=self.dto(original);value['items']*=2
        with self.assertRaises(AssertionError):discovery.validate_discovery(value,{UID:original},SOURCE_ID)
        value['items']=[]
        with self.assertRaises(AssertionError):discovery.validate_discovery(value,{UID:original},SOURCE_ID)
        discovery.validate_discovery(value,{},SOURCE_ID,True)
        value['cluster']='unknown'
        with self.assertRaises(AssertionError):discovery.validate_discovery(value,{},SOURCE_ID,True)
        discovery.validate_discovery(value,{},SOURCE_ID,True,cluster='unknown')

    def test_hidden_empty_exact_dto_rejects_workload_metadata_and_type_leaks(self):
        value=dict(cluster=discovery.CLUSTER,registered=0,ambiguous_workloads=0,items=[])
        discovery.validate_discovery(value,{},SOURCE_ID,True)
        for key,extra in [('source',dict(id=SOURCE_ID)),('uid',UID),('namespace','kube-system'),('image','real:tag'),
                          ('owner_label_hint','team=secret'),('workloads',[dict(uid=UID)])]:
            with self.assertRaises(AssertionError):discovery.validate_discovery(dict(value,**{key:extra}),{},SOURCE_ID,True)
        for key,extra in [('registered',False),('ambiguous_workloads',False),('items',None),('items',[dict(uid=UID)])]:
            with self.assertRaises(AssertionError):discovery.validate_discovery(dict(value,**{key:extra}),{},SOURCE_ID,True)

    def test_denied_actor_counterexample_old_403_vs_actual_200_empty_through_run(self):
        # Native c4ba7960 first red was this exact 200 DTO. Preserve that old
        # mismatch and exercise the candidate's actual request construction.
        empty=dict(ambiguous_workloads=0,cluster=discovery.CLUSTER,items=[],registered=0)
        with self.assertRaises(AssertionError):discovery.check(200==403,'old denied403 assumption rejects legal empty200')
        chain=self.chain();original=self.workload();chain.expected={UID:original}
        chain.roles={'reader':'synthetic-reader','peer':'synthetic-peer'};chain.calls=0;chain.data['helper_sha256']={}
        ledger=dict(applications=0,links=0,audit=[],candidates=0,investigations=0,proposals=0,actions=0,executions=0)
        revoked=False;observed=[]
        def sql(statement,**kw):
            nonlocal revoked
            if statement.startswith('DELETE FROM v3_role_grants'):revoked=True
            return ''
        def call(user,method,path,**kw):
            chain.calls+=1
            status=401 if user is None else 403 if user=='reader' and kw.get('environment')=='staging' else 422 if kw.get('want')==422 else 200
            value=copy.deepcopy(empty) if user in ('denied','hidden') or (user=='peer' and revoked) else self.dto(original)
            if 'does-not-exist' in path:value=dict(empty,cluster='does-not-exist')
            discovery.check(status==kw.get('want',200),'candidate still expects forbidden for a legal hidden response')
            if status==200 and kw.get('validate'):kw['validate'](value)
            observed.append((user,path,kw,status))
            return value,{}
        with contextlib.ExitStack() as stack:
            for name in ('prepare_auth','real_lists','provision','start','seed','login','native_snapshot','marker_case'):
                stack.enter_context(mock.patch.object(chain,name))
            stack.enter_context(mock.patch.object(chain,'ledger',return_value=ledger))
            stack.enter_context(mock.patch.object(chain,'sql',side_effect=sql))
            stack.enter_context(mock.patch.object(chain,'call',side_effect=call))
            result=chain.run()
        denied=next(x for x in observed if x[0]=='denied')
        self.assertEqual(denied[3],200);self.assertNotIn('code',denied[2]);self.assertTrue(denied[2]['validate'])
        self.assertEqual(next(x for x in observed if x[0] is None)[3],401)
        self.assertEqual(next(x for x in observed if x[0]=='reader' and x[2].get('environment')=='staging')[3],403)
        self.assertTrue(revoked);self.assertEqual(observed[-1][0],'peer');self.assertEqual(observed[-1][3],200)
        self.assertFalse(result['counts_as_whole_acceptance'])

    def test_only_first_api_starts_formal_inventory(self):
        chain=self.chain()
        for index in (0,1):
            original=dict(AIOPS_ADDR='127.0.0.1:'+str(discovery.PORTS[index]),DATABASE_URL='private-test-only')
            value=chain.apply_startup_environment(original)
            self.assertEqual(value['DATABASE_URL'],original['DATABASE_URL'])
            self.assertEqual('AIOPS_INVENTORY_CONFIG' in value,index==0)
            self.assertNotIn('AIOPS_PROMETHEUS_CONFIG',value)

    def test_complete_real_15lists_only_projected_receipt_and_no_sql(self):
        chain=self.chain();original=self.workload();original['metadata']['resourceVersion']='1'
        values={path:dict(metadata=dict(resourceVersion='1'),items=[original] if kind=='Deployment' else []) for path,kind in discovery.ENDPOINTS}
        written=[]
        with mock.patch.object(chain,'kube',side_effect=lambda method,path,**kw:copy.deepcopy(values[path.split('?')[0]])) as kube,\
             mock.patch.object(chain,'marker_case'),mock.patch.object(discovery.ro,'_private',side_effect=lambda p,v:written.append((p,copy.deepcopy(v)))),\
             mock.patch.object(discovery,'digest',return_value='hash'),mock.patch.object(chain,'sql') as sql:
            chain.real_lists()
        self.assertEqual(len(kube.call_args_list),15);sql.assert_not_called()
        self.assertEqual(chain.expected,{UID:original})
        receipt=written[0][1];self.assertEqual(len(receipt['counts']),15)
        self.assertEqual(set(receipt['workloads'][0]),{'uid','name','namespace','kind','resource_version','replicas','images'})
        cfg=written[1][1];self.assertEqual(cfg['sources'][0]['application_bindings'],[])
        self.assertEqual(cfg['sources'][0]['grant_permission_ids'],[]);self.assertNotIn('external_snapshot_file',cfg)

    def test_paginated_initial_list_blocks_no_silent_truncation(self):
        chain=self.chain()
        with mock.patch.object(chain,'kube',return_value=dict(metadata=dict(resourceVersion='1',**{'continue':'next'}),items=[])),\
             mock.patch.object(discovery.ro,'_private') as write:
            with self.assertRaises(AssertionError):chain.real_lists()
        write.assert_not_called()

    def test_unique_matching_actual_runner_source_manifest_parent_required(self):
        permit=dict(manifest_sha256='a'*64)
        row=dict(status='running',selected_suite=discovery.MODULE,manifest_sha='a'*64,source_tree_sha=discovery.SOURCE,pid=4321)
        connection=mock.Mock();connection.execute.return_value=[(json.dumps(row),)]
        with mock.patch.object(discovery.sqlite3,'connect',return_value=connection) as connect,\
             mock.patch.object(discovery.os,'getppid',return_value=4321):
            discovery.runner_admission(permit)
            self.assertIn('?mode=ro',connect.call_args.args[0]);self.assertTrue(connect.call_args.kwargs['uri'])
            for field,bad in [('selected_suite','other'),('manifest_sha','b'*64),('source_tree_sha','b'*64),('pid',999)]:
                changed=dict(row,**{field:bad});connection.execute.return_value=[(json.dumps(changed),)]
                with self.assertRaises(AssertionError):discovery.runner_admission(permit)
            connection.execute.return_value=[(json.dumps(row),)]*2
            with self.assertRaises(AssertionError):discovery.runner_admission(permit)

    def test_cleanup_shared_four_second_deadline_uid_preconditions_and_reverse_order(self):
        chain=self.chain();chain.auth=self.records();calls=[]
        def kube(method,path,body=None,**kw):
            calls.append((method,path,body,kw));record=next(x for x in chain.auth if x['path']==path)
            return dict(copy.deepcopy(record['spec']),metadata=dict(record['spec']['metadata'],uid=UID))
        with mock.patch.object(discovery.time,'monotonic',return_value=100),mock.patch.object(chain,'kube',side_effect=kube),\
             mock.patch.object(discovery.ro,'_private'):
            chain.cleanup_auth()
        self.assertEqual(len(calls),9);self.assertEqual({v[3]['deadline'] for v in calls},{104})
        self.assertEqual([v[1] for v in calls[::3]],[v['path'] for v in reversed(chain.auth)])
        for call in calls[1::3]:
            self.assertEqual(call[2]['preconditions'],dict(uid=UID));self.assertEqual(call[0],'DELETE')
        self.assertTrue(all(v[3]['want']==404 for v in calls[2::3]))

    def test_cleanup_uid_drift_never_deletes_and_keeps_receipt(self):
        chain=self.chain();chain.auth=self.records()[:1]
        value=copy.deepcopy(chain.auth[0]['spec']);value['metadata']['uid']='a'*36
        with mock.patch.object(chain,'kube',return_value=value) as kube,mock.patch.object(discovery.ro,'_private') as receipt:
            with self.assertRaises(AssertionError):chain.cleanup_auth()
        self.assertEqual(kube.call_count,1);self.assertEqual(receipt.call_args.args[1]['errors'][0]['uid'],UID)

    def test_unknown_creation_is_retained_without_name_lookup_or_adoption(self):
        chain=self.chain();chain.unknown=['/private-exact-uuid-object']
        with mock.patch.object(chain,'kube') as kube,mock.patch.object(discovery.ro,'_private') as receipt:
            with self.assertRaises(AssertionError):chain.cleanup_auth()
        kube.assert_not_called();self.assertEqual(receipt.call_args.args[1]['unknown_create_paths'],chain.unknown)

    def test_creation_receipt_precedes_post_and_unknown_outcome_never_adopted(self):
        chain=self.chain();events=[];first=RuntimeError('unknown create outcome')
        encoded=base64.b64encode(b'offline synthetic certificate').decode()
        config=dict(**{'current-context':discovery.CONTEXT},clusters=[dict(cluster={'server':discovery.SERVER,'certificate-authority-data':encoded})],
            users=[dict(user={'client-certificate-data':encoded,'client-key-data':encoded})])
        ns=dict(metadata=dict(uid=chain.data['namespace_uid'],labels={'aiops.testlab.owner':discovery.OWNER}),status=dict(phase='Active'))
        def kube(method,path,body=None,**kw):
            events.append((method,path))
            if path=='/api/v1/namespaces/'+discovery.NAMESPACE:return ns
            if method=='GET':self.assertEqual(kw['want'],404);return {}
            self.assertTrue(chain.unknown);self.assertEqual(events[-2][0],'intent')
            raise first
        with mock.patch.object(discovery,'_inspect',return_value=self.info()),mock.patch.object(chain,'frozen'),\
             mock.patch.object(discovery.subprocess,'run',return_value=mock.Mock(returncode=0,stdout=json.dumps(config).encode())) as local,\
             mock.patch.object(discovery.ro,'private_file'),mock.patch.object(discovery.ssl,'create_default_context'),\
             mock.patch.object(discovery.ro,'_private',side_effect=lambda p,v:events.append(('intent',copy.deepcopy(v)))),\
             mock.patch.object(chain,'kube',side_effect=kube):
            with self.assertRaises(RuntimeError) as captured:chain.prepare_auth()
        self.assertIs(captured.exception,first);self.assertEqual(chain.auth,[]);self.assertEqual(len(chain.unknown),1)
        command=local.call_args.args[0]
        self.assertIn(str(discovery.KUBECONFIG),command);self.assertIn(discovery.CONTEXT,command)
        self.assertEqual(command[-6:],['config','view','--raw','--minify','-o','json'])
        self.assertIn('config',command);self.assertNotIn('get',command)

    def test_existing_namespace_uid_mismatch_stops_before_auth_creation(self):
        chain=self.chain();encoded=base64.b64encode(b'offline synthetic certificate').decode()
        config=dict(**{'current-context':discovery.CONTEXT},clusters=[dict(cluster={'server':discovery.SERVER,'certificate-authority-data':encoded})],
            users=[dict(user={'client-certificate-data':encoded,'client-key-data':encoded})])
        ns=dict(metadata=dict(uid='different',labels={'aiops.testlab.owner':discovery.OWNER}),status=dict(phase='Active'))
        with mock.patch.object(discovery,'_inspect',return_value=self.info()),mock.patch.object(chain,'frozen'),\
             mock.patch.object(discovery.subprocess,'run',return_value=mock.Mock(returncode=0,stdout=json.dumps(config).encode())),\
             mock.patch.object(discovery.ro,'private_file'),mock.patch.object(discovery.ssl,'create_default_context'),\
             mock.patch.object(chain,'kube',return_value=ns) as kube:
            with self.assertRaises(AssertionError):chain.prepare_auth()
        self.assertEqual(kube.call_count,1);self.assertEqual(chain.auth,[]);self.assertEqual(chain.unknown,[])

    def test_auth_failure_still_runs_original_cleanup_and_preserves_original_failure(self):
        chain=self.chain();events=[];first=RuntimeError('original cleanup failed');extra=ValueError('auth failed')
        def base(_):events.append('base40');raise first
        def auth():events.append('auth4');raise extra
        with mock.patch.object(discovery.ro.Chain,'cleanup',base),mock.patch.object(chain,'cleanup_auth',side_effect=auth):
            with self.assertRaises(RuntimeError) as captured:chain.cleanup()
        self.assertIs(captured.exception,first);self.assertEqual(events,['base40','auth4'])

    def test_auth_failure_after_successful_base_cleanup_stays_failure(self):
        chain=self.chain();error=RuntimeError('auth deadline')
        with mock.patch.object(discovery.ro.Chain,'cleanup'),mock.patch.object(chain,'cleanup_auth',side_effect=error),\
             mock.patch.object(discovery,'emit') as emit:
            with self.assertRaises(RuntimeError) as captured:chain.cleanup()
        self.assertIs(captured.exception,error);emit.assert_not_called()

    def test_expired_cleanup_deadline_prevents_inspect_and_https(self):
        chain=self.chain()
        with mock.patch.object(discovery.time,'monotonic',return_value=101),mock.patch.object(discovery,'_inspect') as inspect,\
             mock.patch.object(discovery.http.client,'HTTPSConnection') as connection:
            with self.assertRaises(discovery.Blocked):chain.kube('GET',self.records()[0]['path'],admin=True,deadline=100)
        inspect.assert_not_called();connection.assert_not_called()

    def test_cleanup_checks_identity_after_source_drift_without_frozen_gate(self):
        chain=self.chain();chain.auth=self.records()[:1]
        value=copy.deepcopy(chain.auth[0]['spec']);value['metadata']['uid']=UID
        with mock.patch.object(chain,'frozen',side_effect=AssertionError('source drift')) as frozen,\
             mock.patch.object(chain,'kube',return_value=value),mock.patch.object(discovery.ro,'_private'):
            chain.cleanup_auth()
        frozen.assert_not_called()

    def test_private_inventory_and_credential_drift_fails_before_formal_call(self):
        chain=self.chain();chain.private_hashes={chain.work/'inventory.json':'old'};chain.data['helper_sha256']={'helpers':'hash'}
        with mock.patch.object(discovery.ro.Chain,'frozen'),mock.patch.object(discovery,'hashes',return_value={'helpers':'hash'}),\
             mock.patch.object(discovery,'private_regular',side_effect=lambda p:p),mock.patch.object(discovery,'digest',side_effect=['hash','changed']):
            with self.assertRaises(AssertionError):chain.frozen()

    def test_explicit_live_and_admission_before_resource_acquisition(self):
        with mock.patch.object(discovery,'doctor') as doctor,mock.patch.object(discovery.os,'open') as lease:
            with self.assertRaises(SystemExit),contextlib.redirect_stderr(io.StringIO()):discovery.main([])
        doctor.assert_not_called();lease.assert_not_called()

if __name__=='__main__':unittest.main()
