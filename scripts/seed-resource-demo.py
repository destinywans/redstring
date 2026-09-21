#!/usr/bin/env python3
"""Explicit local UI-review seed. Inserts isolated Mock assets; never alters applications.

python3 scripts/seed-resource-demo.py --runtime .local/product-lab/runtime.json \
  --container aiops-canvas-db-1
Use --dry-run to print counts only (reads existing application metadata).
"""
import argparse
import datetime as dt
import json
import os
import subprocess
from urllib.parse import urlparse, unquote

SOURCE = 'ui-review-2026-09-21'

def literal(value):
    if value is None:
        return 'NULL'
    if isinstance(value, bool):
        return 'true' if value else 'false'
    return "'" + str(value).replace("'", "''") + "'"

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', help='Local runtime JSON containing DATABASE_URL')
    parser.add_argument('--container', help='Explicit local PostgreSQL Docker container')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    config = json.load(open(args.runtime)) if args.runtime else {}
    database_url = config.get('DATABASE_URL') or os.environ.get('DATABASE_URL', '')
    url = urlparse(database_url)
    if url.scheme not in ('postgres', 'postgresql') or url.hostname not in ('localhost', '127.0.0.1', '::1', 'db'):
        raise SystemExit('Seed requires an explicit local PostgreSQL DATABASE_URL; remote hosts are refused.')
    env = dict(os.environ)
    env.update(PGPASSWORD=unquote(url.password or ''), PGUSER=unquote(url.username or 'postgres'),
               PGDATABASE=url.path.lstrip('/'), PGHOST=url.hostname or '', PGPORT=str(url.port or 5432))
    if args.container:
        command = ['docker', 'exec', '-i', '-e', 'PGPASSWORD', args.container, 'psql',
                   '-U', env['PGUSER'], '-d', env['PGDATABASE']]
    else:
        command = ['psql']
    command += ['-X', '-v', 'ON_ERROR_STOP=1', '-qAt']
    def sql(statement):
        result = subprocess.run(command, input=statement, text=True, env=env, capture_output=True)
        if result.returncode:
            raise SystemExit(result.stderr.strip())
        return result.stdout.strip()
    raw = sql("""SELECT COALESCE(json_agg(x),'[]') FROM (
      SELECT a.id,a.key,a.name,a.maintainer_id,a.owner_team_id,
      COALESCE((SELECT json_agg(json_build_object('id',e.id,'key',e.key,'name',e.name) ORDER BY e.key)
      FROM application_environments e WHERE e.application_id=a.id AND NOT e.archived),'[]') AS environments
      FROM applications a WHERE NOT a.archived ORDER BY a.key) x;""")
    apps = [a for a in json.loads(raw) if a['environments']]
    if not apps:
        raise SystemExit('No existing application environment; seed will not create or modify applications.')
    preferred = ['lab-checkout-api', 'lab-order-api', 'lab-message-api', 'lab-vm']
    apps.sort(key=lambda a: (preferred.index(a['key']) if a['key'] in preferred else len(preferred), a['key']))
    for app in apps:
        app['env'] = next((e for e in app['environments'] if e['key'] in ('production', 'prod')), app['environments'][0])
    def app_index(key, fallback=0):
        return next((i for i,a in enumerate(apps) if a['key']==key), fallback % len(apps))
    workload_apps = [app_index('lab-checkout-api'), app_index('lab-order-api',1), app_index('lab-message-api',2), app_index('delivery-demo',3)]
    host_app = app_index('lab-vm',3)
    main_environment = apps[workload_apps[0]]['env']['key']
    def cluster_index(app):
        return 0 if app['env']['key'] == main_environment else 1
    now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
    resources = []
    edges = []
    def add(key, kind, name, index=0, **attrs):
        app = apps[index % len(apps)]
        row_id = 'mock-resource-' + key
        minutes = len(resources) * 3
        observed = (now - dt.timedelta(minutes=minutes)).isoformat()
        base = dict(status='Running' if kind in ('pod', 'deployment', 'host', 'node') else 'Healthy',
                    source_label='示例导入', cluster='review-cluster-0'+str(cluster_index(app)+1), namespace='commerce',
                    version='v1.2.0', ip='10.42.0.' + str(len(resources) + 10),
                    cpu_percent=18 + len(resources) % 45, memory_percent=35 + len(resources) % 40,
                    disk_percent=28 + len(resources) % 35, cpu_cores=8, memory_gb=16,
                    age='3d', uptime='3天', labels={'managed-by':'aiops-ui-review','data':'mock'},
                    configuration={'purpose':'界面审阅示例，非真实运行配置'},
                    history=[{'time': (now-dt.timedelta(minutes=5*(11-i))).isoformat(),
                              'cpu': 20 + ((i*7 + len(resources)) % 35),
                              'memory': 42 + ((i*3 + len(resources)) % 22)} for i in range(12)],
                    metrics={'cpu_percent': 18 + len(resources) % 45, 'memory_percent': 35 + len(resources) % 40},
                    logs=['[示例] service initialized', '[示例] health check completed'],
                    events=[{'reason':'Synced','message':'示例资源资料已同步','time':observed,'type':'Normal'}])
        base.update(attrs)
        resources.append(dict(id=row_id, kind=kind, name=name,
                              description='用于 AIOps 页面审阅的 Mock 资源；后续可替换为真实采集数据。',
                              external_id=key, app=app, attributes=base, observed_at=observed))
        return row_id
    app_nodes = []
    for i, app in enumerate(apps):
        app_nodes.append(add('app-' + app['key'], 'application', app['key'], i,
                             status='Registered'))
    clusters = [add('cluster-'+str(i), 'cluster', 'review-cluster-0'+str(i+1), next((j for j,a in enumerate(apps) if cluster_index(a)==i),0),
                    nodes_count=3+i, node_count=3+i, pods_count=12+i*5, pod_count=12+i*5,
                    workload_count=4+i, workloads_count=4+i, provider='Kubernetes', version='v1.34.1') for i in range(2)]
    nodes = [add('node-'+str(i), 'node', 'review-worker-0'+str(i+1), workload_apps[i],
                 roles=['worker'], role='worker', pod_count=4+i, os='Ubuntu 24.04',
                 status='Ready', version='v1.34.1') for i in range(4)]
    deployments = [add('deployment-'+str(i), 'deployment', name, workload_apps[i],
                       workload_type='Deployment', replicas=3, ready_replicas=2 if i==2 else 3,
                       status='Progressing' if i==2 else 'Available',
                       image='registry.example.invalid/review/'+name+':v1.2.0',
                       strategy='RollingUpdate') for i,name in enumerate(['checkout','order','message','gateway'])]
    pods = [add('pod-'+str(i), 'pod', ['checkout','order','message','gateway'][i%4]+'-review-'+str(i), workload_apps[i%4],
                workload=['checkout','order','message','gateway'][i%4], ready='1/1', restarts=2 if i==3 else 0, node='review-worker-0'+str(i%4+1),
                image='registry.example.invalid/review/service:v1.2.0',
                containers=[{'name':'app','image':'registry.example.invalid/review/service:v1.2.0',
                             'status':'Running','restarts':2 if i==3 else 0,'logs':['[示例] HTTP request completed'] }]) for i in range(10)]
    for i in range(5):
        add('event-'+str(i), 'event', 'review-event-'+str(i+1), workload_apps[i%4],
            reason=['Scheduled','Pulled','Started','BackOff','ScalingReplicaSet'][i],
            event_type='Warning' if i==3 else 'Normal', severity='warning' if i==3 else 'info',
            status='Warning' if i==3 else 'Normal', count=3 if i==3 else 1,
            message='[示例] '+['Pod 已调度到工作节点','镜像已准备','容器已启动','容器重启退避','工作负载扩容'][i],
            involved_object='checkout-review-'+str(i), first_seen=(now-dt.timedelta(minutes=30)).isoformat(),last_seen=now.isoformat())
    namespaces = [add('namespace-'+str(i),'namespace',name,workload_apps[i],namespace=name,workloads_count=4,workload_count=4,pods_count=10,pod_count=10,
                       quota={'cpu':'16','memory':'32Gi','pods':50}, cpu_used=4,cpu_total=16,
                       memory_used=8,memory_total=32,pods_used=10,pods_total=50)
                  for i,name in enumerate(['commerce','platform','monitoring'])]
    hosts = [add('host-'+str(i),'host','review-vm-0'+str(i+1),host_app,os='Ubuntu 24.04',region='local-review',
                 role='application',cpu_cores=8,memory_gb=16) for i in range(3)]
    for i in range(4):
        add('network-'+str(i),'network','review-'+('ingress' if i%2 else 'service')+'-'+str(i),workload_apps[i],
            resource_type='Ingress' if i%2 else 'Service',network_type='Ingress' if i%2 else 'Service',
            service_type='LoadBalancer' if i==2 else 'ClusterIP',selector={'app':'checkout'},ports=[80,443] if i%2 else [8080],protocol='TCP',
            address='checkout.example.invalid' if i%2 else '10.43.0.'+str(10+i),workload='checkout')
    storage = [add('storage-'+str(i),'storage','review-data-'+str(i),workload_apps[i],resource_type='PVC',
                   capacity_gb=50,capacity_gib=50,used_gb=12+i,storage_class='local-path',
                   access_mode='ReadWriteOnce',workload=['checkout','order','message','gateway'][i],mount_path='/var/lib/data',pv='review-pv-'+str(i)) for i in range(4)]
    middleware = [add('middleware-'+str(i),kind,name,workload_apps[i],connections=12+i*3,port=port,role='primary',
                      database_size_mb=120+i*40,endpoint=name+'.example.invalid:'+str(port),keys=2048+i*200,partitions=6 if kind=='kafka' else 0,
                      version={'redis':'7.4','mysql':'8.4','kafka':'3.9'}[kind])
                  for i,(kind,name,port) in enumerate([('redis','redis-checkout',6379),('mysql','mysql-orders',3306),
                                                      ('kafka','kafka-messages',9092),('redis','redis-session',6379)])]
    def edge(a,b,kind,provenance='declared'):
        edges.append(dict(id='mock-relation-'+str(len(edges)+1), source=a,target=b,kind=kind,provenance=provenance))
    for i in range(4):
        edge(app_nodes[workload_apps[i]],deployments[i],'deployed_on')
        edge(deployments[i],clusters[cluster_index(apps[workload_apps[i]])],'belongs_to','discovered')
        edge(deployments[i],middleware[i],'depends_on')
        edge(deployments[i],storage[i],'depends_on')
        edge(pods[i],deployments[i],'belongs_to','discovered')
        edge(nodes[i],clusters[cluster_index(apps[workload_apps[i]])],'belongs_to','discovered')
    for i in range(2):
        edge(namespaces[i],clusters[cluster_index(apps[workload_apps[i]])],'belongs_to','discovered')
        edge(hosts[i],middleware[i],'depends_on')
    if args.dry_run:
        print(json.dumps({'source_id':SOURCE,'resources':len(resources),'relations':len(edges),'applications':len(apps),'written':False}))
        return
    statements = ['BEGIN;', "SELECT pg_advisory_xact_lock(752018992);"]
    resource_ids = ','.join(literal(r['id']) for r in resources)
    relation_ids = ','.join(literal(r['id']) for r in edges)
    for table, identifiers in [('cmdb_resources',resource_ids),('cmdb_resource_relations',relation_ids)]:
        statements.append("DO $seed$ BEGIN IF EXISTS (SELECT 1 FROM "+table+" WHERE id IN ("+identifiers+") AND (source_kind<>'mock' OR source_id<>"+literal(SOURCE)+")) THEN RAISE EXCEPTION 'Mock seed ID conflicts with a non-seed record; no data written'; END IF; END; $seed$;")
    for row in resources:
        app=row['app']
        vals=[row['id'],row['kind'],row['name'],row['description'],row['external_id'],'mock',SOURCE,
              app['maintainer_id'],app['owner_team_id'],json.dumps(row['attributes'],ensure_ascii=False),
              json.dumps(['ui-review','mock']),True,row['observed_at']]
        statements.append('INSERT INTO cmdb_resources(id,kind,name,description,external_id,source_kind,source_id,owner_user_id,owner_team_id,attributes,tags,is_mock,observed_at) VALUES ('+
                          ','.join(literal(v) for v in vals)+') ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,attributes=EXCLUDED.attributes,observed_at=EXCLUDED.observed_at,updated_at=now() WHERE cmdb_resources.source_kind=\'mock\' AND cmdb_resources.source_id='+literal(SOURCE)+';')
        # Rebuild only our own associations so corrected seed mappings are idempotent.
        statements.append('DELETE FROM cmdb_resource_applications WHERE resource_id IN (SELECT id FROM cmdb_resources WHERE id='+literal(row['id'])+" AND source_kind='mock' AND source_id="+literal(SOURCE)+');')
        # Bind only rows created by this seed; do not touch foreign or manual IDs.
        statements.append('INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) SELECT id,'+literal(app['id'])+','+literal(app['env']['id'])+
                          ' FROM cmdb_resources WHERE id='+literal(row['id'])+' AND source_kind=\'mock\' AND source_id='+literal(SOURCE)+' ON CONFLICT DO NOTHING;')
        statements.append('INSERT INTO cmdb_resource_changes(resource_id,action,description,is_mock) SELECT id,\'mock.imported\',\'导入界面审阅示例资料；非真实基础设施采集\',true FROM cmdb_resources WHERE id='+literal(row['id'])+
                          ' AND source_kind=\'mock\' AND source_id='+literal(SOURCE)+' AND NOT EXISTS(SELECT 1 FROM cmdb_resource_changes WHERE resource_id='+literal(row['id'])+' AND action=\'mock.imported\');')
    for i, cluster in enumerate(clusters):
        for associated in apps:
            if cluster_index(associated) != i:
                continue
            statements.append('INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) SELECT id,'+literal(associated['id'])+','+literal(associated['env']['id'])+
                              ' FROM cmdb_resources WHERE id='+literal(cluster)+" AND source_kind='mock' AND source_id="+literal(SOURCE)+' ON CONFLICT DO NOTHING;')
    for resource_id in nodes + namespaces:
        record = next(row for row in resources if row['id'] == resource_id)
        for associated in apps:
            if associated['env']['key'] != record['app']['env']['key']:
                continue
            statements.append('INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) SELECT id,'+literal(associated['id'])+','+literal(associated['env']['id'])+
                              ' FROM cmdb_resources WHERE id='+literal(resource_id)+" AND source_kind='mock' AND source_id="+literal(SOURCE)+' ON CONFLICT DO NOTHING;')
    # A shared cache demonstrates many-to-many ownership without cloning the asset.
    if len(apps) > 1:
        other = apps[1]
        statements.append('INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) SELECT id,'+literal(other['id'])+','+literal(other['env']['id'])+
                          ' FROM cmdb_resources WHERE id='+literal(middleware[0])+" AND source_kind='mock' AND source_id="+literal(SOURCE)+' ON CONFLICT DO NOTHING;')
    for relation in edges:
        vals=[relation['id'],relation['source'],relation['target'],relation['kind'],relation['provenance'],'mock',SOURCE,'界面审阅示例关系；不代表已发现真实调用或依赖。',True]
        statements.append('INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id,description,is_mock) SELECT '+','.join(literal(v) for v in vals)+
                          ' WHERE (SELECT count(*) FROM cmdb_resources WHERE id IN ('+literal(relation['source'])+','+literal(relation['target'])+') AND source_kind=\'mock\' AND source_id='+literal(SOURCE)+')=2 ON CONFLICT (id) DO NOTHING;')
    statements.append('COMMIT;')
    sql('\n'.join(statements))
    print(json.dumps({'source_id':SOURCE,'resources':len(resources),'relations':len(edges),'applications':len(apps),'written':True}))

if __name__ == '__main__':
    main()
