"""Offline safety and orchestration checks: all processes/SQL/HTTP are mocked."""
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import identity
import promotion_chain as promotion
from env import _private


class PromotionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        owner = 'testlab-a123456789ab'
        binary = self.root/'.local/testlab/environments'/owner/'backend/aiops'
        binary.parent.mkdir(parents=True); binary.write_bytes(b'verified binary')
        self.backend = dict(owner=owner, binary=str(binary), source_tree_sha='expected',
                            postgres_container='aiops-'+owner+'-pg',
                            database_url='postgres://testlab:private@127.0.0.1:15461/aiops_testlab_app?sslmode=disable')
        self.gitlab = dict(owner=owner, work=str(binary.parent.parent/'gitops'),
                           gitlab_name='aiops-'+owner+'-gitlab', gitlab_url=promotion.GITLAB_ORIGIN,
                           gitlab_image=promotion.GITLAB, gitlab_user_ids={'root':1,'proposer':2,'reviewer':3},
                           tokens={'root':'private-root','proposer':'private-writer','reviewer':'private-reviewer'})
        work = Path(self.gitlab['work']); work.mkdir()
        self.gitlab['ca_file'] = str(work/'tls.crt')
        (work/'tls.crt').write_bytes(b'CA'); os.chmod(work/'tls.crt', 0o600)
        self.chain = promotion.Chain(self.root, self.backend, self.gitlab, 'a'*32)
        self.pginfo = {'Name':'/'+self.backend['postgres_container'],
                       'Config':{'Image':promotion.PG_IMAGE,'Labels':{promotion.LABEL:owner}},
                       'State':{'Running':True},'NetworkSettings':{'Ports':{'5432/tcp':[{'HostIp':'127.0.0.1','HostPort':'15461'}]}}}
        self.gitinfo = {'Name':'/'+self.gitlab['gitlab_name'],
                        'Config':{'Image':promotion.GITLAB,'Labels':{promotion.LABEL:owner}},
                        'State':{'Running':True},'NetworkSettings':{'Ports':{'443/tcp':[{'HostIp':'127.0.0.1','HostPort':'18580'}]}}}
        self.chain.projects['main'] = {'id':7,'name':self.chain.project_name,'namespace_id':1}

    def test_constructor_rejects_foreign_owner_and_binary_before_private_creation(self):
        for changed in [dict(self.backend,owner='../foreign'),dict(self.backend,binary=str(self.root/'other'))]:
            with self.assertRaises(promotion.Blocked):
                promotion.Chain(self.root,changed,self.gitlab,'b'*32)

    def test_work_parent_symlink_rejected(self):
        path = self.chain.work.parent
        other = self.root/'foreign'; other.mkdir()
        linked = self.root/'.local/testlab/environments'/self.backend['owner']/'promotion-other'
        path.rename(linked); path.symlink_to(other,target_is_directory=True)
        with self.assertRaises(promotion.Blocked):
            promotion.Chain(self.root,self.backend,self.gitlab,'b'*32)

    def test_invalid_run_and_duplicate_run_never_reused(self):
        for run in ['abc','../foreign','G'*32]:
            with self.assertRaises(promotion.Blocked):
                promotion.Chain(self.root,self.backend,self.gitlab,run)
        with self.assertRaises(FileExistsError):
            promotion.Chain(self.root,self.backend,self.gitlab,'a'*32)

    def test_state_contains_owner_source_binary_ports_but_no_credentials(self):
        data = json.loads((self.chain.work/'state.json').read_text())
        self.assertEqual(data['owner'],self.backend['owner'])
        self.assertEqual(data['binary_sha256'],hashlib.sha256(b'verified binary').hexdigest())
        for filename in ['state.json','credentials.json']:
            self.assertEqual((self.chain.work/filename).stat().st_mode&0o777,0o600)
        for secret in ['private-root','private-writer','private-reviewer',self.chain.admin['source']['password']]:
            self.assertNotIn(secret,(self.chain.work/'state.json').read_text())

    def test_frozen_source_and_binary_are_independently_checked(self):
        with patch.object(promotion,'source_fingerprint',return_value='expected'):
            self.chain.frozen()
        with patch.object(promotion,'source_fingerprint',return_value='changed'),self.assertRaises(promotion.Blocked):
            self.chain.frozen()
        Path(self.backend['binary']).write_bytes(b'replaced')
        with patch.object(promotion,'source_fingerprint',return_value='expected'),self.assertRaises(promotion.Blocked):
            self.chain.frozen()

    def test_pg_full_identity_tuple_is_required(self):
        promotion.guard_pg(self.pginfo,self.backend)
        mutations = [dict(self.pginfo,Name='/foreign'),dict(self.pginfo,State={'Running':False}),
             dict(self.pginfo,Config={'Image':'foreign','Labels':{promotion.LABEL:self.backend['owner']}}),
             dict(self.pginfo,Config={'Image':promotion.PG_IMAGE,'Labels':{promotion.LABEL:'foreign'}}),
             dict(self.pginfo,NetworkSettings={'Ports':{'5432/tcp':[{'HostIp':'0.0.0.0','HostPort':'15461'}]}})]
        for info in [None,*mutations]:
            with self.subTest(info=info),self.assertRaises(promotion.Blocked):
                promotion.guard_pg(info,self.backend)

    def test_gitlab_full_identity_and_account_tuple_is_required(self):
        promotion.guard_gitlab(self.gitinfo,self.backend,self.gitlab,self.root)
        for info in [None,dict(self.gitinfo,Name='/foreign'),dict(self.gitinfo,State={'Running':False}),
                     dict(self.gitinfo,Config={'Image':'foreign','Labels':{promotion.LABEL:self.backend['owner']}}),
                     dict(self.gitinfo,NetworkSettings={'Ports':{'443/tcp':[{'HostIp':'0.0.0.0','HostPort':'18580'}]}})]:
            with self.assertRaises(promotion.Blocked):
                promotion.guard_gitlab(info,self.backend,self.gitlab,self.root)
        for config in [dict(self.gitlab,owner='foreign'),dict(self.gitlab,work=str(self.root)),
                       dict(self.gitlab,gitlab_url='https://example.com'),dict(self.gitlab,gitlab_user_ids={'root':1,'proposer':2,'reviewer':2})]:
            with self.assertRaises(promotion.Blocked):
                promotion.guard_gitlab(self.gitinfo,self.backend,config,self.root)

    def test_private_credentials_reject_symlink_wide_permissions_foreign_path(self):
        path = Path(self.gitlab['ca_file']); promotion.owned_file(path,path.parent)
        os.chmod(path,0o644)
        with self.assertRaises(promotion.Blocked):promotion.owned_file(path,path.parent)
        os.chmod(path,0o600)
        with self.assertRaises(promotion.Blocked):promotion.owned_file(path,self.root)
        link=path.parent/'link'; link.symlink_to(path)
        with self.assertRaises(promotion.Blocked):promotion.owned_file(link,path.parent)

    def test_sql_cannot_write_any_baseline_or_foreign_database(self):
        with patch.object(self.chain,'command') as command:
            for db in ['aiops_testlab_app','aiops_testlab','wan124test','foreign','postgres']:
                with self.assertRaises(promotion.Blocked):self.chain.pg(db,'DELETE FROM users;')
            command.assert_not_called()

    def test_sql_foreign_container_rejected_before_docker_exec(self):
        with patch.object(promotion,'budgeted_inspect',return_value=dict(self.pginfo,Name='/foreign')),patch.object(self.chain,'command') as command:
            with self.assertRaises(promotion.Blocked):self.chain.pg(self.chain.databases['source'],'SELECT 1',read=True)
            command.assert_not_called()

    def test_sql_read_is_new_explicit_readonly_transaction_and_exact_database(self):
        with patch.object(promotion,'budgeted_inspect',return_value=self.pginfo),patch.object(self.chain,'command',return_value='1') as command:
            self.assertEqual(self.chain.pg(self.chain.databases['production'],'SELECT 1',read=True),'1')
        argv = command.call_args.args[0]
        self.assertEqual(argv[-1],self.chain.databases['production'])
        self.assertEqual(command.call_args.kwargs['input'],'BEGIN READ ONLY; SELECT 1; COMMIT;')
        self.assertNotIn(self.backend['database_url'],' '.join(argv))

    def test_existing_run_database_is_not_reused_or_dropped(self):
        with patch.object(self.chain,'pg',return_value='1') as pg:
            with self.assertRaises(promotion.Blocked):self.chain.create_database('source')
            self.assertEqual(pg.call_count,1)
        self.assertEqual(self.chain.created_dbs,[])

    def test_database_marker_requires_same_owner_run_and_tier(self):
        for marker in ['',self.chain.db_marker('source'),json.dumps({'owner':'foreign'})]:
            with patch.object(self.chain,'pg',return_value=marker),self.assertRaises(promotion.Blocked):
                self.chain.assert_database('production')

    def test_partial_database_create_marker_failure_retains_created_identity(self):
        calls=[]
        def pg(db,statement,**kwargs):
            calls.append(statement)
            if statement.startswith('SELECT count'):return '0'
            if statement.startswith('COMMENT'):raise promotion.Blocked('marker failed')
            return ''
        with patch.object(self.chain,'pg',side_effect=pg),self.assertRaises(promotion.Blocked):
            self.chain.create_database('source')
        self.assertEqual(self.chain.created_dbs,['source'])
        self.assertEqual(self.chain.created_roles,['source'])
        self.assertEqual(len(calls),5)
        self.assertIn('NOSUPERUSER NOCREATEDB NOCREATEROLE',calls[2])
        self.assertIn('OWNER "'+self.chain.db_users['source']+'"',calls[3])

    def test_dsn_exact_loopback_user_port_base_database_and_new_name(self):
        value=self.chain.dsn('production')
        self.assertIn('/'+self.chain.databases['production'],value)
        for bad in ['postgres://testlab:p@example.com:15461/aiops_testlab_app',
                    'postgres://testlab:p@127.0.0.1:5432/aiops_testlab_app',
                    'postgres://real:p@127.0.0.1:15461/aiops_testlab_app',
                    'postgres://testlab:p@127.0.0.1:15461/business']:
            self.chain.backend['database_url']=bad
            with self.assertRaises(promotion.Blocked):self.chain.dsn('production')

    def test_transport_outside_origin_or_path_rejected_before_network(self):
        with patch.object(promotion.http.client,'HTTPSConnection') as network:
            for origin,path in [('https://example.com','/api/v1/x'),('http://localhost:18598','/api/v1/x'),
                         (promotion.GITLAB_ORIGIN,'//example.com'),(promotion.GITLAB_ORIGIN,'/x#token'),
                         (promotion.GITLAB_ORIGIN,'/https://example.com')]:
                with self.assertRaises(promotion.Blocked):promotion.transport(origin,'GET',path)
            network.assert_not_called()

    def test_git_mutation_rejects_old_project_and_unknown_lifecycle(self):
        with patch.object(promotion,'budgeted_inspect',return_value=self.gitinfo),patch.object(promotion,'transport') as network:
            for method,path in [('POST','/api/v4/projects/2/repository/branches'),('DELETE','/api/v4/projects/2'),
                                ('DELETE','/api/v4/users/2'),('POST','/api/v4/users/1/personal_access_tokens'),
                                ('DELETE','/api/v4/personal_access_tokens/1')]:
                with self.assertRaises(promotion.Blocked):self.chain.git(method,path)
            network.assert_not_called()

    def test_git_owned_project_rechecked_before_mutation(self):
        with patch.object(promotion,'budgeted_inspect',return_value=self.gitinfo),patch.object(self.chain,'assert_project') as guard, \
             patch.object(promotion,'transport',return_value=(201,b'{}',{})) as network:
            self.chain.git('POST','/api/v4/projects/7/repository/branches',{'branch':'owned'},role='proposer',want=201)
            guard.assert_called_once_with('main',None)
            self.assertEqual(network.call_args.args[4]['PRIVATE-TOKEN'],'private-writer')

    def test_project_marker_name_namespace_guard_rejects_foreign_id(self):
        project={'id':7,'name':self.chain.project_name,'path':self.chain.project_name,'namespace':{'id':1},
                 'description':self.chain.db_marker('project-main')}
        with patch.object(self.chain,'git',return_value=(project,{})):self.chain.assert_project('main')
        for changed in [dict(project,id=2),dict(project,name='foreign'),dict(project,path='old'),
                        dict(project,namespace={'id':2}),dict(project,description='foreign')]:
            with patch.object(self.chain,'git',return_value=(changed,{})),self.assertRaises(promotion.Blocked):
                self.chain.assert_project('main')

    def policy(self, name='config-a', **extra):
        return {'name':name,'allow_force_push':False,'push_access_levels':[{'access_level':0}],
                'merge_access_levels':[{'access_level':40}],**extra}

    def test_policy_requires_zero_push_no_force_exact_maintainer_and_complete_list(self):
        target='config-a'; branch={'name':target,'protected':True}; safe=self.policy()
        promotion.safe_policy(branch,safe,[safe],target)
        for policy in [self.policy(allow_force_push=True),self.policy(push_access_levels=[{'access_level':30}]),
                       self.policy(push_access_levels=[{'access_level':0,'user_id':2}]),
                       self.policy(merge_access_levels=[{'access_level':30}]),self.policy(merge_access_levels=[]),
                       self.policy(merge_access_levels=[{'access_level':40,'group_id':1}])]:
            with self.assertRaises(AssertionError):promotion.safe_policy(branch,policy,[policy],target)
        for branch in [{'name':'wrong','protected':True},{'name':target,'protected':False}]:
            with self.assertRaises(AssertionError):promotion.safe_policy(branch,safe,[safe],target)
        with self.assertRaises(AssertionError):promotion.safe_policy({'name':target,'protected':True},safe,[],target)

    def test_permissive_matching_wildcard_and_unknown_syntax_fail_closed(self):
        safe=self.policy(); branch={'name':'config-a','protected':True}
        for rule in [self.policy('config-*',push_access_levels=[{'access_level':30}]),self.policy('*',allow_force_push=True),self.policy('config-?')]:
            with self.assertRaises(AssertionError):promotion.safe_policy(branch,safe,[safe,rule],'config-a')
        promotion.safe_policy(branch,safe,[safe,self.policy('other-*',allow_force_push=True)],'config-a')

    def test_policy_equality_ignores_branch_commit_that_legitimately_changes_on_merge(self):
        safe=self.policy(); base={'name':'config-a','protected':True}
        self.assertEqual(promotion.safe_policy(base|{'commit':{'id':'a'}},safe,[safe],'config-a'),
                         promotion.safe_policy(base|{'commit':{'id':'b'}},safe,[safe],'config-a'))

    def review(self):
        return ({'iid':1,'sha':'1'*40,'author':{'id':2},'state':'merged','merged_at':'2026-09-30T01:00:03Z'},
                [{'head_commit_sha':'1'*40,'state':'collected','created_at':'2026-09-30T01:00:01Z'}],
                {'approved':True,'approved_by':[{'user':{'id':3},'approved_at':'2026-09-30T01:00:02Z'}]},
                {'id':3,'bot':False,'state':'active'})

    def test_review_exact_latest_sha_and_strict_before_after_times(self):
        mr,versions,approvals,reviewer=self.review()
        self.assertEqual(promotion.review_evidence(mr,versions,approvals,reviewer)['reviewed_sha'],'1'*40)
        for version in [dict(versions[0],state='collecting'),dict(versions[0],head_commit_sha='2'*40),
                        dict(versions[0],created_at='2026-09-30T01:00:02Z')]:
            with self.assertRaises(AssertionError):promotion.review_evidence(mr,[version],approvals,reviewer)
        for when in ['2026-09-30T01:00:01Z','2026-09-30T01:00:03Z','2026-09-30T01:00:04Z','bad']:
            changed={'approved':True,'approved_by':[{'user':{'id':3},'approved_at':when}]}
            with self.assertRaises(AssertionError):promotion.review_evidence(mr,versions,changed,reviewer)
        with self.assertRaises(AssertionError):promotion.review_evidence(mr,versions+[dict(versions[0],created_at='2026-09-30T01:00:04Z')],approvals,reviewer)

    def test_review_rejects_author_self_approval_bot_disabled_missing(self):
        mr,versions,approvals,reviewer=self.review()
        for reviewer in [{'id':2,'bot':False,'state':'active'},{'id':3,'bot':True,'state':'active'},
                         {'id':3,'bot':False,'state':'blocked'},{'id':3,'state':'active'}]:
            with self.assertRaises(AssertionError):promotion.review_evidence(mr,versions,approvals,reviewer)
        for approvals in [{'approved':True,'approved_by':[]},{'approved':False,'approved_by':[]},
                          {'approved':True,'approved_by':[{'user':{'id':2},'approved_at':'2026-09-30T01:00:02Z'}]}]:
            with self.assertRaises(AssertionError):promotion.review_evidence(mr,versions,approvals,self.review()[3])

    def manifest(self):
        graph={'kind':'plan','name':'Synthetic <structure>', 'nodes':[{'id':'node-1','x':16,'y':140,'w':260,
               'icon':'layers','tone':'info','title':'节点 1','sub':'','blocks':[]}], 'edges':[]}
        graph_raw=json.dumps(graph,ensure_ascii=False,separators=(',',':')).encode().replace(b'<',b'\\u003c').replace(b'>',b'\\u003e')
        return {'schema':1,'promotion_id':'a'*32,'kind':'template','source_id':'CT-owned','source_instance':self.chain.source_id,
                'source_environment':'test','source_digest':hashlib.sha256(graph_raw).hexdigest(),'template':graph,
                'signature':base64.urlsafe_b64encode(b's'*64).rstrip(b'=').decode()}

    def test_signed_bytes_reconstruct_exact_go_struct_order_utf8_html_escaping(self):
        manifest=self.manifest()
        # PostgreSQL JSONB may sort all object keys; the signature verifier must
        # restore every nested struct's order before invoking Ed25519.
        scrambled=json.loads(json.dumps(manifest,sort_keys=True))
        raw=self.chain.signed_bytes(scrambled)
        self.assertEqual(raw,self.chain.signed_bytes(manifest))
        self.assertTrue(raw.startswith(b'{"schema":1,"promotion_id":'))
        self.assertTrue(raw.endswith(b',"signature":""}'))
        self.assertIn('节点 1'.encode(),raw)
        self.assertIn(b'\\u003cstructure\\u003e',raw)
        self.assertNotIn(manifest['signature'].encode(),raw)

    def test_signature_verification_binds_graph_digest_and_exact_openssl_public_key(self):
        manifest=self.manifest()
        with patch.object(self.chain,'command',return_value='Signature Verified Successfully') as command:
            self.chain.verify_signature(manifest)
        argv=command.call_args.args[0]
        self.assertEqual(argv[:4],['openssl','pkeyutl','-verify','-pubin'])
        self.assertEqual(argv[argv.index('-inkey')+1],str(self.chain.work/'signer-public.pem'))
        manifest['template']['nodes'][0]['w']=200
        with patch.object(self.chain,'command') as command,self.assertRaises(AssertionError):
            self.chain.verify_signature(manifest)
        command.assert_not_called()

    def test_review_merge_captures_policy_both_sides_and_binds_exact_sha(self):
        mr,versions,approvals,reviewer=self.review()
        opened=mr|{'state':'opened','merged_at':None,'source_project_id':7,'target_project_id':7,
                   'source_branch':self.chain.prefix+'a'*32,'target_branch':self.chain.target}
        merged=opened|{'state':'merged','merged_at':mr['merged_at'],'merge_commit_sha':'2'*40}
        requests=[];reads=[0]
        def git(method,path,body=None,**kwargs):
            requests.append((method,path,body,kwargs))
            if path.endswith('/versions?per_page=100&page=1'):return versions,{}
            if path.endswith('/approvals'):return approvals,{}
            if path=='/api/v4/users/3':return reviewer,{}
            if method=='GET':
                reads[0]+=1;return (merged if reads[0]>=3 else opened),{}
            return {},{}
        policy={'exact':{'push':0,'merge':40,'force':False}}
        item={'id':'a'*32,'iid':1}
        with patch.object(self.chain,'assert_project'),patch.object(self.chain,'git',side_effect=git),patch.object(self.chain,'policy',return_value=policy):
            self.chain.review_merge(item)
        writes=[row for row in requests if row[0]!='GET']
        self.assertEqual([row[1].rsplit('/',1)[1] for row in writes],['approve','merge'])
        self.assertTrue(all(row[2]['sha']=='1'*40 and row[3]['role']=='reviewer' for row in writes))
        self.assertEqual(item['merge_sha'],'2'*40)
        after=json.loads((self.chain.work/'review-after-1-main.json').read_text())
        self.assertEqual(after['policy_before'],after['policy_after'])
        self.assertEqual(after['reviewed_sha'],'1'*40)

    def test_reviewed_sha_move_blocks_merge_after_approval(self):
        mr,versions,approvals,reviewer=self.review()
        mr=mr|{'state':'opened','source_project_id':7,'target_project_id':7,
               'source_branch':self.chain.prefix+'a'*32,'target_branch':self.chain.target}
        reads=[0];writes=[]
        def git(method,path,body=None,**kwargs):
            if path.endswith('/versions?per_page=100&page=1'):return versions,{}
            if path.endswith('/approvals'):return approvals,{}
            if path=='/api/v4/users/3':return reviewer,{}
            if method=='GET':
                reads[0]+=1;return (mr if reads[0]==1 else mr|{'sha':'3'*40}),{}
            writes.append(path);return {},{}
        with patch.object(self.chain,'assert_project'),patch.object(self.chain,'git',side_effect=git),patch.object(self.chain,'policy',return_value={}),self.assertRaises(AssertionError):
            self.chain.review_merge({'id':'a'*32,'iid':1})
        self.assertEqual(len(writes),1);self.assertTrue(writes[0].endswith('/approve'))

    def test_all_named_scenarios_make_valid_runner_interface_cells(self):
        for scenario in ['normal','idempotency','concurrency','response-loss','unmerged','bad-signature','wrong-path','unprotected','wrong-project-lookup']:
            status=200 if scenario in ('normal','idempotency','concurrency') else 503
            with patch.object(self.chain,'frozen'),patch.object(promotion,'transport',return_value=(status,b'{}',{})),patch.object(promotion,'emit') as emit:
                self.chain.call('production','POST','/api/v1/config-sync/mr-2/import',want=status,scenario=scenario)
                cell=emit.call_args.kwargs['interface']
                self.assertEqual(cell['path'],'/api/v1/config-sync/{id}/import')
                self.assertIn(promotion.scenario_kind(scenario),cell['scenarios'])
                self.assertEqual(cell['scenarios'][promotion.scenario_kind(scenario)]['actual_status'],status)

    def test_bad_http_first_red_is_immutable_and_sanitized(self):
        with patch.object(self.chain,'frozen'),patch.object(promotion,'transport',return_value=(503,b'{"error":{"code":"untrusted","message":"private-secret"}}',{})),patch.object(promotion,'emit'):
            for wanted in [200,201]:
                with self.assertRaises(AssertionError):self.chain.call('production','POST','/api/v1/config-sync/mr-2/import',want=wanted)
        row=json.loads((self.chain.work/'first-failure.json').read_text())
        self.assertEqual(row['expected']['status'],[200])
        self.assertEqual(row['path'],'/api/v1/config-sync/{id}/import')
        self.assertNotIn('private-secret',json.dumps(row))

    def test_401_403_404_never_count_as_route_registration(self):
        for code in [401,403,404]:
            with patch.object(self.chain,'frozen'),patch.object(promotion,'transport',return_value=(code,b'{}',{})),patch.object(promotion,'emit') as emit:
                self.chain.call('production','GET','/api/v1/config-sync',want=code,scenario='authorization')
                self.assertFalse(emit.call_args.kwargs['registration_evidence'])

    def test_exact_drop_path_and_single_armed_operation(self):
        proxy=promotion.TLSProxy.__new__(promotion.TLSProxy)
        proxy.lock=threading.Lock(); proxy.drop=None
        for path in ['/api/v1/config-sync/mr-0/import','/api/v1/config-sync/{id}/import','/api/v1/promotions','/api/v1/config-sync/mr-1/import?x=y']:
            with self.assertRaises(promotion.Blocked):proxy.arm_drop('POST',path)
        with self.assertRaises(promotion.Blocked):proxy.arm_drop('GET','/api/v1/config-sync/mr-1/import')
        proxy.arm_drop('POST','/api/v1/config-sync/mr-1/import')
        with self.assertRaises(promotion.Blocked):proxy.arm_drop('POST','/api/v1/config-sync/mr-2/import')

    def test_start_uses_generated_production_config_readonly_token_and_exact_dsn(self):
        self.chain.configure('production')
        config=json.loads((self.chain.work/'production-sync.json').read_text())
        self.assertEqual(config['tier'],'production')
        self.assertEqual(list(config['trusted_sources']),[self.chain.source_id])
        self.assertNotIn('signing_key_file',config)
        proc=Mock(pid=123);proc.poll.return_value=None
        response=Mock(status=200); connection=Mock(); connection.getresponse.return_value=response
        with patch.object(self.chain,'frozen'),patch.object(self.chain,'assert_database'),patch.object(promotion,'free_port'), \
             patch.object(promotion.subprocess,'Popen',return_value=proc) as start,patch.object(promotion.http.client,'HTTPConnection',return_value=connection):
            self.chain.start('production')
        env=start.call_args.kwargs['env']
        self.assertIn('/'+self.chain.databases['production'],env['DATABASE_URL'])
        self.assertEqual(env['AIOPS_ADDR'],'127.0.0.1:18599')
        self.assertEqual(env['AIOPS_V3_INSTANCE_CONFIG'],str(self.chain.work/'production-instance.json'))
        self.assertEqual(env['AIOPS_V3_CONFIG_SYNC_CONFIG'],str(self.chain.work/'production-sync.json'))
        self.assertNotIn('AIOPS_V3_ALERT_RULE_TRIAL_CONFIG',env)

    def test_cleanup_uses_held_handles_and_stops_before_database_mutation(self):
        order=[]
        self.chain.created_dbs=['source']; self.chain.projects.clear()
        self.chain.processes['source']=Mock(pid=123)
        def stop(tier,deadline):
            order.append('stop-'+tier);self.chain.processes[tier]=None;return {'exit_code':0}
        def pg(db,statement,**kwargs):
            order.append(statement)
            if statement.startswith('SELECT json_build_object'):
                return json.dumps({'present':False,'owner_marker':None,'connections':0,'active_drop_commands':0})
            return '0'
        with patch.object(self.chain,'stop',side_effect=stop),patch.object(self.chain,'assert_database') as guard, \
             patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit'):
            self.chain.cleanup()
        guard.assert_called_once()
        self.assertEqual(order[:2],['stop-source','stop-production'])
        self.assertTrue(order[-2].startswith('DROP DATABASE'))
        self.assertTrue(order[-1].startswith('SELECT json_build_object'))
        self.assertNotIn('pg_terminate_backend',' '.join(order))

    def test_drop_timeout_recovers_only_after_new_read_proves_absence_and_no_pending_command(self):
        self.chain.created_dbs=['source'];self.chain.projects.clear()
        calls=[]
        def pg(db,statement,**kwargs):
            calls.append((db,statement,kwargs))
            if statement.startswith('DROP DATABASE'):
                raise subprocess.TimeoutExpired(['private-do-not-serialize'],8)
            if statement.startswith('SELECT json_build_object'):
                return json.dumps({'present':False,'owner_marker':None,'connections':0,'active_drop_commands':0})
            return '0'
        with patch.object(self.chain,'stop',return_value={'already_exited':True}),patch.object(self.chain,'assert_database') as guard, \
             patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit') as emit:
            self.chain.cleanup()
        guard.assert_called_once()
        self.assertEqual(sum(s.startswith('DROP DATABASE') for _,s,_ in calls),1)
        self.assertTrue(calls[-1][2]['read']);self.assertTrue(calls[-1][2]['admin'])
        self.assertEqual(len({kwargs['deadline'] for _,_,kwargs in calls}),1)
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['errors'],[])
        db=row['databases'][0]
        self.assertTrue(db['dropped']);self.assertTrue(db['timeout_recovered'])
        self.assertEqual(db['drop_error'],{'stage':'drop-database','error_type':'TimeoutExpired','command_timeout_seconds':8})
        self.assertNotIn('private-do-not-serialize',json.dumps(row))
        emit.assert_called_once();self.assertEqual(emit.call_args.args[1],'passed')

    def test_drop_timeout_retains_pending_or_still_present_database_without_second_delete(self):
        for state in [dict(present=False,owner_marker=None,connections=0,active_drop_commands=1),
                      dict(present=False,owner_marker=None,connections=1,active_drop_commands=0),
                      dict(present=True,owner_marker=self.chain.db_marker('source'),connections=0,active_drop_commands=0),
                      dict(present=True,owner_marker='foreign',connections=0,active_drop_commands=0)]:
            with self.subTest(state=state):
                self.chain.created_dbs=['source'];self.chain.projects.clear();calls=[]
                def pg(db,statement,**kwargs):
                    calls.append(statement)
                    if statement.startswith('DROP DATABASE'):raise subprocess.TimeoutExpired(['private'],8)
                    if statement.startswith('SELECT json_build_object'):return json.dumps(state)
                    return '0'
                with patch.object(self.chain,'stop',return_value={'already_exited':True}),patch.object(self.chain,'assert_database'), \
                     patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit') as emit:
                    with self.assertRaises(promotion.Blocked):self.chain.cleanup()
                self.assertEqual(sum(s.startswith('DROP DATABASE') for s in calls),1)
                row=json.loads((self.chain.work/'resource-end.json').read_text())
                self.assertFalse(row['databases'][0]['dropped'])
                self.assertEqual(row['databases'][0]['readback'],state)
                self.assertEqual(row['databases'][0]['drop_error']['stage'],'drop-database')
                self.assertEqual(row['errors'][0]['stage'],'drop-readback');emit.assert_not_called()

    def test_drop_readback_timeout_remains_red_preserves_drop_timeout_and_attempts_next_resource(self):
        self.chain.created_dbs=['source','production'];self.chain.projects.clear();calls=[]
        def pg(db,statement,**kwargs):
            calls.append(statement)
            if self.chain.databases['source'] in statement:
                if statement.startswith('DROP DATABASE'):raise subprocess.TimeoutExpired(['drop'],8)
                if statement.startswith('SELECT json_build_object'):raise subprocess.TimeoutExpired(['readback'],3)
            if statement.startswith('SELECT json_build_object'):
                return json.dumps({'present':False,'owner_marker':None,'connections':0,'active_drop_commands':0})
            return '0'
        with patch.object(self.chain,'stop',return_value={'already_exited':True}),patch.object(self.chain,'assert_database'), \
             patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit') as emit:
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['errors'][0]['stage'],'drop-readback')
        self.assertEqual(row['errors'][0]['command_timeout_seconds'],3)
        self.assertEqual(row['databases'][0]['drop_error']['command_timeout_seconds'],8)
        self.assertTrue(row['databases'][1]['dropped'])
        self.assertEqual(sum(s.startswith('DROP DATABASE') for s in calls),2);emit.assert_not_called()

    def test_marker_timeout_never_issues_drop_and_records_exact_stage(self):
        self.chain.created_dbs=['source'];self.chain.projects.clear()
        with patch.object(self.chain,'stop',return_value={'already_exited':True}), \
             patch.object(self.chain,'assert_database',side_effect=subprocess.TimeoutExpired(['private'],3)), \
             patch.object(self.chain,'pg') as pg,patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        pg.assert_not_called()
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['errors'][0]['stage'],'owner-marker')
        self.assertEqual(row['errors'][0]['command_timeout_seconds'],3)

    def test_cleanup_refuses_mismarked_db_and_still_attempts_other_owned_resource(self):
        self.chain.created_dbs=['source','production'];self.chain.projects.clear()
        def guard(tier,deadline):
            if tier=='source':raise promotion.Blocked('foreign marker')
        def read(db,statement,**kwargs):
            if statement.startswith('SELECT json_build_object'):
                return json.dumps({'present':False,'owner_marker':None,'connections':0,'active_drop_commands':0})
            return '0'
        with patch.object(self.chain,'stop',return_value={'already_exited':True}),patch.object(self.chain,'assert_database',side_effect=guard), \
             patch.object(self.chain,'pg',side_effect=read) as pg,patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        self.assertFalse(any(self.chain.databases['source'] in call.args[1] for call in pg.call_args_list))
        self.assertTrue(any('DROP DATABASE' in call.args[1] for call in pg.call_args_list))
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['errors'][0]['resource'],'database-source')
        self.assertTrue(row['databases'][0]['dropped'])

    def test_active_connections_retained_without_kill_or_forced_drop(self):
        self.chain.created_dbs=['production'];self.chain.projects.clear()
        with patch.object(self.chain,'stop',return_value={'already_exited':True}),patch.object(self.chain,'assert_database'), \
             patch.object(self.chain,'pg',return_value='1') as pg,patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        self.assertEqual(pg.call_count,1)
        self.assertNotIn('DROP',pg.call_args.args[1])

    def test_failed_stop_handle_blocks_database_drop(self):
        self.chain.created_dbs=['production'];self.chain.projects.clear();self.chain.processes['production']=Mock()
        def stop(tier,deadline):
            if tier=='production':raise promotion.Blocked('stop failed')
        with patch.object(self.chain,'stop',side_effect=stop),patch.object(self.chain,'pg') as pg,patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        pg.assert_not_called()

    def test_foreign_project_cleanup_refuses_delete_but_continues_process_cleanup(self):
        with patch.object(self.chain,'stop',return_value={'already_exited':True}) as stop,patch.object(self.chain,'assert_project',side_effect=promotion.Blocked('foreign project')), \
             patch.object(self.chain,'git') as git,patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        self.assertEqual(stop.call_count,2);git.assert_not_called()

    def test_foreign_token_cleanup_is_not_revoked(self):
        self.chain.projects.clear(); self.chain.tokens.append({'id':42,'name':'promotion-owned','user_id':3})
        with patch.object(self.chain,'stop',return_value={'already_exited':True}),patch.object(self.chain,'git',return_value=({'id':42,'name':'foreign','user_id':3,'scopes':['api']},{})) as git,patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        self.assertEqual(git.call_count,1);self.assertEqual(git.call_args.args[0],'GET')

    def test_primary_and_cleanup_errors_preserved_separately_first_red_immutable(self):
        self.chain.run=Mock(side_effect=AssertionError('product red'))
        self.chain.cleanup=Mock(side_effect=promotion.Blocked('cleanup red'))
        with patch.object(identity,'emit'),self.assertRaisesRegex(AssertionError,'product red'):
            identity.execute(self.chain)
        self.assertTrue((self.chain.work/'primary-error.json').exists())
        self.assertTrue((self.chain.work/'cleanup-error.json').exists())
        self.assertEqual(json.loads((self.chain.work/'first-failure.json').read_text())['error'],'product red')

    def test_sigterm_during_cleanup_finishes_bounded_cleanup_and_never_passes(self):
        cleaned=[]; self.chain.run=Mock(return_value={'done':True})
        def cleanup():
            signal.getsignal(signal.SIGTERM)(signal.SIGTERM,None);cleaned.append(True)
        self.chain.cleanup=cleanup
        with patch.object(identity,'emit'),self.assertRaises(promotion.Blocked):identity.execute(self.chain)
        self.assertEqual(cleaned,[True])
        self.assertTrue((self.chain.work/'cleanup-interrupted.json').exists())

    def test_normal_orchestration_reaches_parallel_import_response_drop_restart_and_negatives(self):
        # No database/process/HTTP call: exercise the actual sequencing and the
        # concurrent import closure with independent fresh object identities.
        objects=[]; order=[]
        def export(suffix):
            value={'id':str(len(objects)+1).zfill(32),'iid':len(objects)+1,'manifest':{},'path':'owned'}
            objects.append(value);return value
        def import_item(item,scenario='normal'):
            order.append(('import',item['iid'],scenario))
            proxy=self.chain.proxies['production']
            if scenario=='response-loss':
                proxy.drop_receipt={'path':'/api/v1/config-sync/mr-'+str(item['iid'])+'/import','response_sha256':'hash'}
                raise http.client.RemoteDisconnected()
            return {'id':item['iid'],'status':'imported'}
        negative_number=[0]
        def negative(original,scenario,project='main'):
            negative_number[0]+=1;return {'id':'9'*31+str(negative_number[0]),'iid':10+negative_number[0]}
        proxy=Mock();proxy.drop_receipt=None;self.chain.proxies['production']=proxy
        with patch.object(self.chain,'setup'),patch.object(self.chain,'export',side_effect=export), \
             patch.object(self.chain,'review_merge',return_value={'reviewed_sha':'1'*40}), \
             patch.object(self.chain,'call',return_value=({},{})),patch.object(self.chain,'counts',return_value={'target':0,'ledger':0,'audit':0}), \
             patch.object(self.chain,'list_state'),patch.object(self.chain,'verify_import'),patch.object(self.chain,'import_item',side_effect=import_item), \
             patch.object(self.chain,'stop',return_value={'already_exited':True}) as stop,patch.object(self.chain,'start') as start,patch.object(self.chain,'login'), \
             patch.object(self.chain,'negative_mr',side_effect=negative),patch.object(self.chain,'create_project'), \
             patch.object(self.chain,'configure'),patch.object(self.chain,'adversarial_protocols'), \
             patch.object(self.chain,'frozen'),patch.object(promotion,'emit') as emit:
            result=self.chain.run()
        self.assertEqual(result['normal_objects'],3)
        self.assertEqual(sum(1 for item in order if item[2]=='concurrency'),2)
        self.assertEqual(sum(1 for item in order if item[2]=='response-loss-retry'),1)
        self.assertEqual(start.call_count,2);self.assertEqual(stop.call_count,2)
        self.assertTrue(self.chain.completed)
        remaining=[call for call in emit.call_args_list if call.args[0]=='promotion.remaining']
        self.assertEqual(len(remaining),1)
        self.assertEqual(remaining[0].args[1],'external_todo')
        self.assertEqual(remaining[0].args[2]['unverified'],['enterprise historical governance and physical reviewer identity'])

    def proxy(self):
        proxy=promotion.ControlledGitLabProxy.__new__(promotion.ControlledGitLabProxy)
        proxy.chain=self.chain;proxy.project_id=7;proxy.writer='owned-writer';proxy.reader='owned-reader'
        proxy.lock=threading.Lock();proxy.fault=None;proxy.receipts=[];proxy.events=[];proxy.errors=[]
        return proxy

    def test_controlled_proxy_rejects_foreign_credentials_origins_project_and_admin_endpoints(self):
        proxy=self.proxy()
        for method,path,token in [('GET','/api/v4/projects/2/merge_requests','owned-reader'),
                                 ('GET','https://example.com/api/v4/projects/7/merge_requests','owned-reader'),
                                 ('GET','//example.com/api/v4/projects/7/merge_requests','owned-reader'),
                                 ('GET','/api/v4/users/1','owned-reader'),('GET','/api/v4/projects/7/members','owned-reader'),
                                 ('GET','/api/v4/projects/7/merge_requests','root-token'),
                                 ('DELETE','/api/v4/projects/7/merge_requests/1','owned-writer'),
                                 ('POST','/api/v4/projects/7/repository/branches?branch=x&ref=y','owned-reader')]:
            with self.subTest(path=path),self.assertRaises(promotion.Blocked):proxy.guard_request(method,path,None,token)

    def test_controlled_proxy_writes_require_exact_run_branch_template_path_target_label(self):
        proxy=self.proxy();branch=self.chain.prefix+'b'*32
        branch_path='/api/v4/projects/7/repository/branches?'+promotion.urllib.parse.urlencode({'branch':branch,'ref':self.chain.target})
        role,details=proxy.guard_request('POST',branch_path,None,'owned-writer')
        self.assertEqual(role,'writer');self.assertEqual(details['stage'],'branch')
        for suffix in ['branch=foreign&ref='+self.chain.target,'branch='+branch+'&ref=main',
                       'branch='+branch+'&ref='+self.chain.target+'&extra=1',
                       'branch='+branch+'&branch='+branch+'&ref='+self.chain.target]:
            with self.assertRaises(promotion.Blocked):proxy.guard_request('POST','/api/v4/projects/7/repository/branches?'+suffix,None,'owned-writer')
        mr={'source_branch':branch,'target_branch':self.chain.target,'title':'Owned','labels':'aiops-config-promotion'}
        proxy.guard_request('POST','/api/v4/projects/7/merge_requests',json.dumps(mr).encode(),'owned-writer')
        for change in [dict(mr,source_branch='foreign'),dict(mr,target_branch='main'),dict(mr,labels='deploy'),dict(mr,extra='unknown')]:
            with self.assertRaises(promotion.Blocked):proxy.guard_request('POST','/api/v4/projects/7/merge_requests',json.dumps(change).encode(),'owned-writer')
        manifest={'promotion_id':'b'*32,'source_instance':self.chain.source_id,'kind':'template','source_id':'CT-'+'c'*32}
        value={'branch':branch,'content':json.dumps(manifest),'commit_message':'Owned'}
        path='/api/v4/projects/7/repository/files/'+promotion.urllib.parse.quote('ops-config/templates/'+'b'*32+'.json',safe='')
        _,details=proxy.guard_request('POST',path,json.dumps(value).encode(),'owned-writer')
        self.assertEqual(details['source_id'],manifest['source_id'])
        for changed in [dict(value,branch=self.chain.prefix+'a'*32),dict(value,extra='unknown'),
                        dict(value,content=json.dumps(manifest|{'source_instance':'foreign'}))]:
            with self.assertRaises(promotion.Blocked):proxy.guard_request('POST',path,json.dumps(changed).encode(),'owned-writer')
        with self.assertRaises(promotion.Blocked):proxy.guard_request('POST',path.replace('ops-config','outside'),json.dumps(value).encode(),'owned-writer')

    def test_controlled_proxy_drops_only_real_201_at_exact_armed_stage(self):
        proxy=self.proxy();template='CT-'+'c'*32;branch=self.chain.prefix+'b'*32
        proxy.arm_drop('branch',template)
        details={'stage':'branch','branch':branch,'target':self.chain.target}
        resource={'name':branch,'commit':{'id':'1'*40}}
        path='/api/v4/projects/7/repository/branches'
        for status in [400,409,500]:
            _,drop=proxy.process_response('POST',path,details,status,json.dumps({'message':'failed'}).encode(),'writer')
            self.assertFalse(drop);self.assertEqual(proxy.receipts,[])
        _,drop=proxy.process_response('POST',path,details,201,json.dumps(resource).encode(),'reader')
        self.assertFalse(drop)
        _,drop=proxy.process_response('POST',path,details,201,json.dumps(resource).encode(),'writer')
        self.assertTrue(drop);self.assertIsNone(proxy.fault)
        self.assertEqual(proxy.receipts[0]['resource'],resource)
        self.assertEqual(proxy.receipts[0]['source_id'],template)
        self.assertEqual(proxy.receipts[0]['upstream_status'],201)
        _,drop=proxy.process_response('POST',path,details,201,json.dumps(resource).encode(),'writer')
        self.assertFalse(drop);self.assertEqual(len(proxy.receipts),1)

    def test_mr_drop_requires_preceding_same_template_file_acceptance(self):
        proxy=self.proxy();template='CT-'+'c'*32;branch=self.chain.prefix+'b'*32
        proxy.arm_drop('merge_request',template)
        details={'stage':'merge_request','branch':branch,'target':self.chain.target}
        raw=json.dumps({'iid':12}).encode()
        with self.assertRaises(AssertionError):proxy.process_response('POST','/api/v4/projects/7/merge_requests',details,201,raw,'writer')
        proxy.events.append({'stage':'file','upstream_status':201,'branch':branch,'source_id':template})
        _,drop=proxy.process_response('POST','/api/v4/projects/7/merge_requests',details,201,raw,'writer')
        self.assertTrue(drop)

    def test_controlled_proxy_never_changes_signed_raw_file_bytes(self):
        proxy=self.proxy();raw=b'{ "schema":1, "web_url":"https://localhost:18580/raw", "signature":"exact" }\n'
        output,drop=proxy.process_response('GET','/api/v4/projects/7/repository/files/ops-config%2Ftemplate.json/raw?ref=abc',{},200,raw,'reader')
        self.assertEqual(output,raw);self.assertFalse(drop)
        self.assertEqual(proxy.events[-1]['response_sha256'],hashlib.sha256(raw).hexdigest())

    def test_transparent_proxy_maps_only_web_url_authority_not_identity_manifest_or_other_urls(self):
        proxy=self.proxy();original={'iid':12,'source_project_id':7,'target_project_id':7,
            'web_url':promotion.GITLAB_ORIGIN+'/root/owned/-/merge_requests/12',
            'content':'literal '+promotion.GITLAB_ORIGIN+'/private','external_url':'https://example.com'}
        output,drop=proxy.process_response('GET','/api/v4/projects/7/merge_requests/12',{},200,json.dumps(original).encode(),'reader')
        actual=json.loads(output)
        self.assertEqual(actual,original|{'web_url':promotion.GITLAB_PROXY_ORIGIN+'/root/owned/-/merge_requests/12'})
        self.assertFalse(drop);self.assertEqual(proxy.receipts,[])

    def test_cross_project_fault_requires_real_owned_alternative_and_single_fault(self):
        proxy=self.proxy()
        for iid,foreign in [(0,8),(12,0),(12,7),(True,8),(12,99)]:
            with self.assertRaises(promotion.Blocked):proxy.arm_cross_project(iid,foreign)
        self.chain.projects['foreign']={'id':8,'name':'own-foreign','namespace_id':1}
        with patch.object(self.chain,'assert_project') as guard:
            proxy.arm_cross_project(12,8);guard.assert_called_once_with('foreign')
            with self.assertRaises(promotion.Blocked):proxy.arm_drop('branch','CT-'+'c'*32)
        proxy.clear();self.assertIsNone(proxy.fault)

    def test_cross_project_modifies_only_selected_actual_200_list_and_detail_identity(self):
        proxy=self.proxy();proxy.fault={'kind':'cross_project','iid':12,'foreign_project':8}
        mr={'iid':12,'source_project_id':7,'target_project_id':7,'state':'merged','sha':'1'*40,'merge_commit_sha':'2'*40,
            'source_branch':self.chain.prefix+'b'*32,'target_branch':self.chain.target}
        other=mr|{'iid':13}
        for path,original in [('/api/v4/projects/7/merge_requests?state=merged',[mr,other]),('/api/v4/projects/7/merge_requests/12',mr)]:
            output,drop=proxy.process_response('GET',path,{},200,json.dumps(original).encode(),'reader')
            actual=json.loads(output);target=actual[0] if isinstance(actual,list) else actual
            self.assertEqual(target,mr|{'source_project_id':8,'target_project_id':8})
            if isinstance(actual,list):self.assertEqual(actual[1],other)
            self.assertFalse(drop)
        self.assertEqual(len(proxy.receipts),2)
        self.assertTrue(all(item['upstream_status']==200 and item['original_merge_sha']=='2'*40 for item in proxy.receipts))

    def test_cross_project_cannot_claim_404_nonexistent_wrong_iid_or_writer_response(self):
        proxy=self.proxy();proxy.fault={'kind':'cross_project','iid':12,'foreign_project':8}
        mr={'iid':12,'source_project_id':7,'target_project_id':7,'state':'merged'}
        for status,path,role,raw in [(404,'/api/v4/projects/7/merge_requests/12','reader',b'{"message":"404"}'),
                      (200,'/api/v4/projects/7/merge_requests/13','reader',json.dumps(mr).encode()),
                      (200,'/api/v4/projects/7/merge_requests/12','writer',json.dumps(mr).encode())]:
            proxy.process_response('GET',path,{},status,raw,role)
        self.assertEqual(proxy.receipts,[])
        with self.assertRaises(AssertionError):proxy.process_response('GET','/api/v4/projects/7/merge_requests/12',{},200,
                               json.dumps(mr|{'source_project_id':99}).encode(),'reader')

    def test_proxy_config_requires_held_run_proxy_and_main_project(self):
        with self.assertRaises(promotion.Blocked):self.chain.configure('source',origin=promotion.GITLAB_PROXY_ORIGIN)
        self.chain.git_proxy=self.proxy()
        self.chain.configure('source',origin=promotion.GITLAB_PROXY_ORIGIN)
        config=json.loads((self.chain.work/'source-sync.json').read_text())
        self.assertEqual(config['gitlab']['base_url'],promotion.GITLAB_PROXY_ORIGIN)
        for origin in ['https://example.com','http://localhost:18604','https://localhost:18605']:
            with self.assertRaises(promotion.Blocked):self.chain.configure('source',origin=origin)
        with self.assertRaises(promotion.Blocked):self.chain.configure('production','unprotected',origin=promotion.GITLAB_PROXY_ORIGIN)

    def loss_fixture(self, stage):
        template='CT-'+'c'*32;pid='b'*32;branch=self.chain.prefix+pid
        record={'id':pid,'branch':branch,'path':'ops-config/templates/'+pid+'.json','state':'uncertain','attempt_generation':1,
                'mr_iid':None,'payload':{'source_id':template},'source_digest':'digest'}
        resource=({'iid':12,'source_project_id':7,'target_project_id':7,'source_branch':branch,'target_branch':self.chain.target}
                  if stage=='merge_request' else {'name':branch,'commit':{'id':'1'*40}})
        before=({'head':'2'*40,'commits':['2'*40,'1'*40],'mr_iids':[12],'file_sha256':'file'} if stage=='merge_request'
                else {'head':'1'*40,'commits':['1'*40],'mr_iids':[],'file_sha256':None})
        after={'head':'2'*40,'commits':['2'*40,'1'*40],'mr_iids':[12],'file_sha256':'file'}
        proxy=Mock(receipts=[{'source_id':template,'stage':stage,'upstream_status':201,'branch':branch,'resource':resource}],errors=[],
                   events=[{'stage':write,'method':'POST','upstream_status':201} for write in ('branch','file','merge_request')])
        self.chain.git_proxy=proxy
        return template,record,before,after

    def test_accepted_upstream_branch_and_mr_loss_reconcile_exact_identity_and_audit(self):
        for stage in ['branch','merge_request']:
            with self.subTest(stage=stage):
                template,record,before,after=self.loss_fixture(stage)
                retry={'mr':'!12','path':record['path']}
                with patch.object(self.chain,'make_template',return_value=template), \
                     patch.object(self.chain,'call',side_effect=[({'error':{'code':'config_sync_unavailable'}},{}),(retry,{}),(retry,{})]), \
                     patch.object(self.chain,'source_record',side_effect=[record,record|{'state':'exported','mr_iid':12,'attempt_generation':2}]), \
                     patch.object(self.chain,'remote_snapshot',side_effect=[before,after]),patch.object(self.chain,'list_state'), \
                     patch.object(self.chain,'pg',return_value=json.dumps({'requested':2,'unknown':1,'completed':1})), \
                     patch.object(self.chain,'counts',return_value={'target':0,'ledger':0,'audit':0}),patch.object(promotion,'emit') as emit:
                    self.chain.upstream_response_loss(stage)
                self.assertEqual(emit.call_args.kwargs['evidence_mode'],'controlled_protocol_fault')
                self.assertTrue((self.chain.work/('upstream-loss-'+stage+'.json')).exists())

    def test_upstream_loss_extra_commit_fails_without_success_artifact(self):
        template,record,before,after=self.loss_fixture('merge_request');after['commits'].insert(0,'3'*40)
        with patch.object(self.chain,'make_template',return_value=template), \
             patch.object(self.chain,'call',side_effect=[({'error':{'code':'config_sync_unavailable'}},{}),({'mr':'!12','path':record['path']},{})]), \
             patch.object(self.chain,'source_record',side_effect=[record,record|{'state':'exported','mr_iid':12,'attempt_generation':2}]), \
             patch.object(self.chain,'remote_snapshot',side_effect=[before,after]),patch.object(promotion,'emit') as emit:
            with self.assertRaisesRegex(AssertionError,'silently created'):self.chain.upstream_response_loss('merge_request')
        emit.assert_not_called();self.assertFalse((self.chain.work/'upstream-loss-merge_request.json').exists())

    def test_direct_upstream_duplicate_mr_evidence_persisted_before_assertion(self):
        _,record,_,_=self.loss_fixture('merge_request')
        branch={'name':record['branch'],'commit':{'id':'1'*40}}
        duplicate=[{'iid':12},{'iid':13}]
        with patch.object(self.chain,'assert_project'),patch.object(self.chain,'git',side_effect=[(branch,{}),(duplicate,{})]):
            with self.assertRaisesRegex(AssertionError,'duplicate MRs'):self.chain.remote_snapshot(record,True)
        evidence=list(self.chain.work.glob('upstream-readback-*.json'))
        self.assertEqual(len(evidence),1)
        self.assertEqual(json.loads(evidence[0].read_text())['mrs'],duplicate)

    def test_cleanup_closes_controlled_proxy_before_stopping_held_processes(self):
        self.chain.projects.clear();order=[]
        self.chain.git_proxy=Mock();self.chain.git_proxy.close.side_effect=lambda:order.append('proxy')
        with patch.object(self.chain,'stop',side_effect=lambda tier,deadline:order.append(tier) or {}),patch.object(promotion,'emit'):
            self.chain.cleanup()
        self.assertEqual(order,['proxy','source','production'])

    def test_controlled_proxy_partial_tls_setup_failure_closes_only_held_server(self):
        promotion.private_bytes(self.chain.work/'source-token',b'owned-writer')
        promotion.private_bytes(self.chain.work/'production-token',b'owned-reader')
        server=Mock();ctx=Mock();ctx.load_cert_chain.side_effect=OSError('synthetic TLS fixture error')
        with patch.object(self.chain,'assert_project'),patch.object(promotion,'free_port'), \
             patch.object(promotion,'ThreadingHTTPServer',return_value=server),patch.object(promotion.ssl,'SSLContext',return_value=ctx):
            with self.assertRaises(OSError):promotion.ControlledGitLabProxy(self.chain)
        server.server_close.assert_called_once()
        server.shutdown.assert_not_called()

    def test_full_adversarial_orchestration_proves_control_before_injection_and_restores_same_object(self):
        order=[];pid='b'*32;control={'id':pid,'iid':12,'merge_sha':'2'*40}
        proxy=Mock(errors=[],receipts=[{'path':path,'upstream_status':200,'original_merge_sha':'2'*40,
                  'forged_source_project':8,'forged_target_project':8}
                 for path in ['/api/v4/projects/7/merge_requests','/api/v4/projects/7/merge_requests/12']])
        proxy.arm_cross_project.side_effect=lambda *args:order.append('arm')
        proxy.clear.side_effect=lambda:order.append('clear')
        def call(*args,**kwargs):
            order.append('deny');self.assertEqual(kwargs['want'],503)
            return {'error':{'code':'config_source_untrusted'}},{}
        with patch.object(promotion,'ControlledGitLabProxy',return_value=proxy), \
             patch.object(self.chain,'stop',return_value={}) as stop,patch.object(self.chain,'start') as start, \
             patch.object(self.chain,'configure') as configure,patch.object(self.chain,'login'), \
             patch.object(self.chain,'export',side_effect=lambda *args:order.append('export-control') or control), \
             patch.object(self.chain,'upstream_response_loss') as losses,patch.object(self.chain,'review_merge'), \
             patch.object(self.chain,'create_project',return_value=8), \
             patch.object(self.chain,'list_state',side_effect=lambda *args:order.append('positive-list')), \
             patch.object(self.chain,'counts',return_value={'target':0,'ledger':0,'audit':0}), \
             patch.object(self.chain,'call',side_effect=call),patch.object(self.chain,'import_item',side_effect=lambda *args:order.append('import')), \
             patch.object(self.chain,'verify_import'),patch.object(promotion,'emit'):
            self.chain.adversarial_protocols()
        self.assertEqual([call.args[0] for call in losses.call_args_list],['branch','merge_request'])
        self.assertEqual(order,['export-control','positive-list','arm','deny','deny','clear','positive-list','import'])
        self.assertEqual(start.call_count,4);self.assertEqual(stop.call_count,4)
        proxy.arm_cross_project.assert_called_once_with(12,8)
        self.assertEqual([call.kwargs.get('origin',promotion.GITLAB_ORIGIN) for call in configure.call_args_list],
                         [promotion.GITLAB_PROXY_ORIGIN,promotion.GITLAB_PROXY_ORIGIN,promotion.GITLAB_ORIGIN,promotion.GITLAB_ORIGIN])
        evidence=json.loads((self.chain.work/'cross-project-schema.json').read_text())
        self.assertTrue(evidence['control_positive_before_injection'])
        self.assertEqual(evidence['production_rows_after_denial'],{'target':0,'ledger':0,'audit':0})

    def test_isolated_api_dsn_never_reuses_control_database_or_superuser(self):
        for tier in promotion.PORTS:
            parsed=promotion.urllib.parse.urlsplit(self.chain.dsn(tier))
            self.assertEqual(parsed.username,self.chain.db_users[tier])
            self.assertEqual(parsed.password,self.chain.db_passwords[tier])
            self.assertNotEqual(parsed.password,'private')
            self.assertEqual((parsed.hostname,parsed.port,parsed.path),
                             ('127.0.0.1',15461,'/'+self.chain.databases[tier]))
            query=dict(promotion.urllib.parse.parse_qsl(parsed.query))
            self.assertEqual(query['application_name'],'promotion-api-'+tier+'-'+self.chain.run_id)
            self.assertEqual(query['sslmode'],'disable')
        with patch.object(promotion,'budgeted_inspect',return_value=self.pginfo),patch.object(self.chain,'command',return_value='0') as command:
            self.chain.pg(self.chain.databases['source'],'SELECT 1',read=True)
            argv=command.call_args.args[0]
            self.assertEqual(argv[argv.index('-U')+1],self.chain.db_users['source'])
            self.chain.pg('postgres','SELECT 1',read=True,admin=True)
            argv=command.call_args.args[0]
            self.assertEqual(argv[argv.index('-U')+1],'testlab')

    def test_existing_role_is_never_adopted_or_modified(self):
        with patch.object(self.chain,'pg',side_effect=['0','1']) as pg:
            with self.assertRaisesRegex(promotion.Blocked,'role already exists'):
                self.chain.create_database('source')
        self.assertEqual(self.chain.created_roles,[])
        self.assertTrue(all(call.kwargs.get('read') for call in pg.call_args_list))

    def test_replaced_exact_cid_is_rejected_before_sql_or_gitlab_request(self):
        self.chain.backend['promotion_pg_cid']='expected-pg'
        self.chain.gitlab['promotion_cid']='expected-gitlab'
        with patch.object(promotion,'budgeted_inspect',return_value=self.pginfo|{'Id':'foreign'}),patch.object(self.chain,'command') as command:
            with self.assertRaises(promotion.Blocked):
                self.chain.pg('postgres','SELECT 1',read=True,admin=True)
            command.assert_not_called()
        with patch.object(promotion,'budgeted_inspect',return_value=self.gitinfo|{'Id':'foreign'}),patch.object(promotion,'transport') as network:
            with self.assertRaises(promotion.Blocked):self.chain.git('GET','/api/v4/user')
            network.assert_not_called()
        with self.assertRaises(promotion.Blocked):
            promotion.guard_gitlab(self.gitinfo|{'Id':'foreign'},self.backend,self.gitlab,self.root)

    def test_popen_handle_remains_registered_when_state_write_fails(self):
        proc=Mock(pid=12345)
        with patch.object(self.chain,'frozen'),patch.object(self.chain,'assert_database'),patch.object(promotion,'free_port'), \
             patch.object(promotion.subprocess,'Popen',return_value=proc),patch.object(self.chain,'save',side_effect=OSError('state write failed')):
            with self.assertRaises(OSError):self.chain.start('source')
        self.assertIs(self.chain.processes['source'],proc)
        self.assertEqual(self.chain.state['pids']['source'],12345)

    def test_tls_certificate_failure_closes_registered_unstarted_listener(self):
        server=Mock();ctx=Mock();ctx.load_cert_chain.side_effect=OSError('certificate fixture')
        with patch.object(promotion,'ThreadingHTTPServer',return_value=server),patch.object(promotion.ssl,'SSLContext',return_value=ctx):
            with self.assertRaises(OSError):promotion.TLSProxy(self.chain,'source')
        self.assertIs(self.chain.proxies['source'].server,server)
        server.server_close.assert_called_once();server.shutdown.assert_not_called()

    def test_controlled_proxy_state_failure_closes_registered_started_thread(self):
        promotion.private_bytes(self.chain.work/'source-token',b'owned-writer')
        promotion.private_bytes(self.chain.work/'production-token',b'owned-reader')
        server=Mock();ctx=Mock();thread=Mock(ident=123)
        thread.is_alive.side_effect=[True,False]
        with patch.object(self.chain,'assert_project'),patch.object(promotion,'free_port'), \
             patch.object(promotion,'ThreadingHTTPServer',return_value=server),patch.object(promotion.ssl,'SSLContext',return_value=ctx), \
             patch.object(promotion.threading,'Thread',return_value=thread),patch.object(promotion.ControlledGitLabProxy,'save',side_effect=OSError('state write failed')):
            with self.assertRaises(OSError):promotion.ControlledGitLabProxy(self.chain)
        self.assertIs(self.chain.git_proxy.server,server)
        server.shutdown.assert_called_once();server.server_close.assert_called_once();thread.join.assert_called_once()

    def test_role_marker_mismatch_preserves_role_but_cleans_other_owned_role(self):
        self.chain.projects.clear();self.chain.created_roles=['source','production'];calls=[]
        def pg(db,statement,**kwargs):
            calls.append(statement)
            if statement.startswith('SELECT COALESCE'):
                return 'foreign' if self.chain.db_users['source'] in statement else self.chain.db_marker('role-production')
            return '0'
        with patch.object(self.chain,'stop',return_value={}),patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        drops=[s for s in calls if s.startswith('DROP ROLE')]
        self.assertEqual(drops,['DROP ROLE "'+self.chain.db_users['production']+'";'])
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['errors'][0]['resource'],'role-source')
        self.assertTrue(row['roles'][0]['absent'])

    def test_role_drop_timeout_is_never_replayed_and_absence_is_read_back(self):
        self.chain.projects.clear();self.chain.created_roles=['source'];calls=[]
        def pg(db,statement,**kwargs):
            calls.append(statement)
            if statement.startswith('SELECT COALESCE'):return self.chain.db_marker('role-source')
            if statement.startswith('DROP ROLE'):raise subprocess.TimeoutExpired(['private'],8)
            return '0'
        with patch.object(self.chain,'stop',return_value={}),patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit'):
            self.chain.cleanup()
        self.assertEqual(sum(s.startswith('DROP ROLE') for s in calls),1)
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertTrue(row['roles'][0]['absent']);self.assertTrue(row['roles'][0]['timeout_recovered'])

    def test_project_delete_accepted_needs_api_absence_after_independent_role_cleanup(self):
        self.chain.created_roles=['source'];order=[]
        def git(method,path,body=None,**kwargs):
            order.append(method)
            return ({'message':'404 Project Not Found'},{},404) if method=='GET' else ({},{})
        def pg(db,statement,**kwargs):
            order.append('role')
            return self.chain.db_marker('role-source') if statement.startswith('SELECT COALESCE') else '0'
        with patch.object(self.chain,'stop',return_value={}),patch.object(self.chain,'assert_project',return_value={'path_with_namespace':'root/owned'}), \
             patch.object(self.chain,'git',side_effect=git),patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit'):
            self.chain.cleanup()
        self.assertEqual(order[0],'DELETE');self.assertEqual(order[-1],'GET')
        self.assertIn('role',order[1:-1])
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertTrue(row['projects'][0]['api_absent'])
        self.assertTrue(row['projects'][0]['delete_scheduled'])

    def test_sigint_and_sigterm_during_popen_acquisition_retain_handle_before_cancellation(self):
        for sig in (signal.SIGINT,signal.SIGTERM):
            with self.subTest(signal=sig):
                self.chain.processes.clear();proc=Mock(pid=12345);previous=signal.getsignal(sig)
                def acquire(*args,**kwargs):
                    signal.raise_signal(sig)
                    return proc
                with patch.object(self.chain,'frozen'),patch.object(self.chain,'assert_database'),patch.object(promotion,'free_port'), \
                     patch.object(promotion.subprocess,'Popen',side_effect=acquire):
                    with self.assertRaisesRegex(promotion.Blocked,'acquisition interrupted'):
                        self.chain.start('source')
                self.assertIs(self.chain.processes['source'],proc)
                self.assertEqual(signal.getsignal(sig),previous)

    def test_sigint_during_listener_acquisition_registers_and_closes_exact_server(self):
        server=Mock()
        def acquire(*args,**kwargs):
            signal.raise_signal(signal.SIGINT)
            return server
        with patch.object(promotion,'ThreadingHTTPServer',side_effect=acquire):
            with self.assertRaisesRegex(promotion.Blocked,'acquisition interrupted'):
                promotion.TLSProxy(self.chain,'source')
        self.assertIs(self.chain.proxies['source'].server,server)
        server.server_close.assert_called_once();server.shutdown.assert_not_called()

    def test_create_database_timeout_records_intent_and_retains_unmarked_exact_database(self):
        calls=[]
        def create(db,statement,**kwargs):
            calls.append(statement)
            if statement.startswith('CREATE DATABASE'):raise subprocess.TimeoutExpired(['private'],8)
            return '0'
        with patch.object(self.chain,'pg',side_effect=create):
            with self.assertRaises(subprocess.TimeoutExpired):self.chain.create_database('source')
        self.assertEqual(self.chain.created_dbs,['source']);self.assertEqual(self.chain.pending_dbs,['source'])
        self.assertEqual(sum(s.startswith('CREATE DATABASE') for s in calls),1)
        self.chain.projects.clear();calls=[]
        def cleanup(db,statement,**kwargs):
            calls.append(statement)
            if statement.startswith('SELECT json_build_object'):
                return json.dumps({'present':True,'owner':self.chain.db_users['source'],'active_create_commands':0})
            if statement.startswith('SELECT COALESCE'):return self.chain.db_marker('role-source')
            if 'datdba=' in statement:return '1'
            return ''
        with patch.object(self.chain,'stop',return_value={}),patch.object(self.chain,'pg',side_effect=cleanup),patch.object(promotion,'emit'):
            with self.assertRaises(promotion.Blocked):self.chain.cleanup()
        self.assertFalse(any(s.startswith(('CREATE','DROP')) for s in calls))
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['databases'][0]['creation_intent_readback']['owner'],self.chain.db_users['source'])
        self.assertFalse(row['databases'][0]['dropped'])
        self.assertEqual({e['resource'] for e in row['errors']},{'database-source','role-source'})

    def test_uncommitted_database_intent_is_read_back_without_database_ddl_replay(self):
        self.chain.projects.clear();self.chain.created_dbs=['source'];self.chain.pending_dbs=['source'];self.chain.created_roles=['source'];calls=[]
        def pg(db,statement,**kwargs):
            calls.append(statement)
            if statement.startswith('SELECT json_build_object'):
                return json.dumps({'present':False,'owner':None,'active_create_commands':0})
            if statement.startswith('SELECT COALESCE'):return self.chain.db_marker('role-source')
            return '0'
        with patch.object(self.chain,'stop',return_value={}),patch.object(self.chain,'pg',side_effect=pg),patch.object(promotion,'emit'):
            self.chain.cleanup()
        self.assertFalse(any(s.startswith(('CREATE DATABASE','DROP DATABASE')) for s in calls))
        self.assertEqual(sum(s.startswith('DROP ROLE') for s in calls),1)
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertTrue(row['databases'][0]['creation_not_committed_or_already_absent'])

    def test_http200_with_404_message_does_not_prove_project_absent(self):
        responses=[(202,b'{}',{}),(202,b'{}',{}),(200,b'{"message":"404 Project Not Found"}',{}),
                   (404,b'{"message":"404 Project Not Found"}',{})]
        with patch.object(self.chain,'stop',return_value={}),patch.object(self.chain,'assert_project',return_value={'path_with_namespace':'root/owned'}), \
             patch.object(promotion,'budgeted_inspect',return_value=self.gitinfo),patch.object(promotion,'transport',side_effect=responses) as transport, \
             patch.object(promotion.time,'sleep'),patch.object(promotion,'emit'):
            self.chain.cleanup()
        self.assertEqual(transport.call_count,4)
        row=json.loads((self.chain.work/'resource-end.json').read_text())
        self.assertEqual(row['projects'][0]['readback_status'],404)

    def test_marked_project_cleanup_accepts_only_exact_run_deletion_rename(self):
        name=self.chain.project_name+'-deletion_scheduled-7'
        marked={'id':7,'name':name,'path':name,'path_with_namespace':'root/'+name,'namespace':{'id':1},
                'description':self.chain.db_marker('project-main'),'marked_for_deletion_on':'2026-10-01'}
        with patch.object(self.chain,'git',return_value=(marked,{})):
            self.assertEqual(self.chain.assert_project('main',deleting=True),marked)
        for change in [dict(marked,id=8),dict(marked,name=name+'-foreign'),dict(marked,path='foreign'),
                       dict(marked,path_with_namespace='other/'+name),dict(marked,marked_for_deletion_on=None),
                       dict(marked,description='foreign')]:
            with patch.object(self.chain,'git',return_value=(change,{})),self.assertRaises(promotion.Blocked):
                self.chain.assert_project('main',deleting=True)

    def test_permanent_project_remove_requires_actual_owned_marked_full_path(self):
        project={'path_with_namespace':'root/'+self.chain.project_name+'-deletion_scheduled-7'}
        with patch.object(promotion,'budgeted_inspect',return_value=self.gitinfo),patch.object(self.chain,'assert_project',return_value=project), \
             patch.object(promotion,'transport',return_value=(202,b'{}',{})) as network:
            for body in [{'permanently_remove':True,'full_path':'root/foreign'},
                         {'permanently_remove':True,'full_path':project['path_with_namespace'],'extra':True}]:
                with self.assertRaises(promotion.Blocked):self.chain.git('DELETE','/api/v4/projects/7',body,want=202)
            network.assert_not_called()
            self.chain.git('DELETE','/api/v4/projects/7',{'permanently_remove':True,'full_path':project['path_with_namespace']},want=202)
            network.assert_called_once()


if __name__=='__main__':
    unittest.main()
