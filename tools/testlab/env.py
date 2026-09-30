"""Owned, disposable local services. Never reads the product .env."""
from __future__ import annotations
import json, os, re, secrets, shutil, socket, subprocess, tempfile, time, urllib.parse, uuid
from pathlib import Path

PG_IMAGE = "pgvector/pgvector:0.8.6-pg17"
PORT = 15461
LABEL = "aiops.testlab.owner"

def _cmd(argv, *, input=None, timeout=90):
    p = subprocess.run(argv, input=input, text=True, capture_output=True, timeout=timeout)
    if p.returncode:
        # Do not print argv: credentials are sometimes command arguments.
        raise RuntimeError(f"{argv[0]} failed ({p.returncode}): {p.stderr[-1200:]}")
    return p.stdout.strip()

def _private(path: Path, data):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.is_symlink():raise ValueError('refusing symlink for private testlab state')
    fd, temporary = tempfile.mkstemp(prefix='.'+path.name+'-',dir=path.parent)
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(data, f, indent=2);f.flush();os.fsync(f.fileno())
        os.replace(temporary,path)
    finally:
        if os.path.exists(temporary):os.unlink(temporary)

def _inspect(container,kind=None):
    p=subprocess.run(['docker']+([kind] if kind else [])+['inspect',container],text=True,capture_output=True,timeout=15)
    if p.returncode:
        if any(x in p.stderr.lower() for x in ('no such object','no such container','no such network')):return None
        raise RuntimeError('cannot inspect owned Docker resource')
    records=json.loads(p.stdout)
    if len(records)!=1:raise RuntimeError('invalid Docker resource response')
    return records[0]

def _guard(root,data):
    owner=data.get('owner','')
    if not re.fullmatch(r'testlab-[a-z0-9]+',owner):raise ValueError('invalid testlab owner')
    work=Path(data.get('work','')).resolve()
    if work != root/'.local/testlab/environments'/owner or data.get('container')!='aiops-'+owner+'-pg':
        raise ValueError('testlab resource ownership path/name mismatch')
    return work

def _owner(info,owner):
    if info is not None and info.get('Config',{}).get('Labels',info.get('Labels',{})).get(LABEL)!=owner:
        raise ValueError('testlab ownership label mismatch')

def _port_open(port):
    with socket.socket() as s:
        s.settimeout(.2)
        return s.connect_ex(('127.0.0.1',port))==0

def _owned_record(path,owner):
    if path.is_symlink():raise ValueError('refusing symlink for owned testlab state')
    if not path.exists():return None
    if path.is_symlink() or not path.is_file():raise ValueError('refusing nonregular owned testlab state')
    data=json.loads(path.read_text())
    if data.get('owner')!=owner:raise ValueError('testlab component state owner mismatch')
    return data

def _stop_backend(data):
    import backend
    backend.stop(data)

def _cleanup_components(root):
    import components
    return components.cleanup(root)

def _heavy_resources(root, owner, work):
    data=_owned_record(root/'.local/testlab/gitops-environment.json',owner)
    if not data:return [], None
    if Path(data.get('work','')).resolve()!=work/'gitops' or data.get('gitlab_name')!='aiops-'+owner+'-gitlab' or data.get('cluster')!='aiops-backend-testlab':
        raise ValueError('GitOps resource ownership path/name mismatch')
    if Path(data.get('kubeconfig','')).resolve()!=work/'gitops/kubeconfig':
        raise ValueError('GitOps kubeconfig is outside the owned directory')
    return [data['gitlab_name'],data['cluster']+'-control-plane'],data

def public_snapshot(root):
    """Small nonsecret recorded environment, with a live PG ownership check."""
    root=Path(root).resolve();state=root/'.local/testlab/environment.json'
    if state.is_symlink():raise ValueError('private environment state cannot be a symlink')
    if not state.exists():return {'kind':'testlab','status':'unprepared','data_source':'no owned environment record'}
    data=json.loads(state.read_text());work=_guard(root,data);info=_inspect(data['container']);_owner(info,data['owner'])
    if info is None or not info.get('State',{}).get('Running'):raise ValueError('owned PG is unavailable')
    databases=[]
    for key,database in {'TEST_DATABASE_URL':'aiops_testlab','TESTLAB_MFA_DSN':'wan124mfa','TESTLAB_PASSKEY_DSN':'wan124passkey','TESTLAB_FORMAL_DSN':'wan124test','TESTLAB_APP_DSN':'aiops_testlab_app'}.items():
        u=urllib.parse.urlparse(data.get('env',{}).get(key,''))
        if u.scheme not in ('postgres','postgresql') or u.hostname!='127.0.0.1' or u.port!=PORT or u.path!='/'+database:raise ValueError('recorded test database is outside the dedicated loopback contract')
        databases.append(database)
    result={'kind':'testlab','status':'recorded','owner':data['owner'],'environment_run_id':data.get('run_id'),'postgres':{'name':data['container'],'image':info.get('Config',{}).get('Image'),'image_id':info.get('Image'),'port':PORT,'live_owner_verified':True,'databases':databases},'data_source':'synthetic seed 20260930; migrations include explicitly synthetic old versions; model/protocol mocks do not prove external tenants'}
    fixture=_owned_record(root/'.local/testlab/backend-fixture.json',data['owner'])
    if fixture:result['backend']={k:fixture[k] for k in ('source_tree_sha','runtime_sha','seed','base_urls') if k in fixture}
    import components
    result['configured_local_component_versions']=components.IMAGES
    result['component_liveness_scope']='versions from controlled configuration; each component requires its own runtime result'
    heavy=_owned_record(root/'.local/testlab/gitops-environment.json',data['owner'])
    if heavy:result['configured_gitops']={k:heavy[k] for k in ('gitlab_image','cluster','complete_rule_enable_chain') if k in heavy}
    return result

def _process_port(pid,port):
    binary=shutil.which('lsof')
    if not binary:return False
    p=subprocess.run([binary,'-nP','-iTCP:'+str(port),'-sTCP:LISTEN','-Fp'],capture_output=True,text=True,timeout=5)
    return 'p'+str(pid) in p.stdout.splitlines()

def _process_ports(root,owner,work):
    ports={}
    backend_data=_owned_record(root/'.local/testlab/backend-fixture.json',owner)
    if backend_data:
        import backend
        current=backend.identity()
        if current and current.get('owner')==owner and current.get('pid')==backend_data.get('supervisor_pid'):
            candidate={18560:current['pid']}
            for port,pid in zip((18561,18562),current.get('api_pids',[])):candidate[port]=pid
            runtime_data=_owned_record(root/'.local/testlab/runtime-config.json',owner)
            for proxy in (runtime_data or {}).get('proxies',[]):candidate[proxy['port']]=current['pid']
            for port,pid in candidate.items():
                if _process_port(pid,port):ports[port]='verified backend process '+str(pid)
    component_data=_owned_record(work/'component-environment.json',owner)
    if component_data and component_data.get('fixture_pid'):
        import components
        pid=component_data['fixture_pid']
        if components.fixture_alive(pid,owner,work):
            for port in (18569,18577,18578,18579):
                if _process_port(pid,port):ports[port]='HMAC-verified fixture process '+str(pid)
    return ports

def doctor(root):
    root=Path(root).resolve()
    rows = []
    for name in ("docker", "go", "python3", "openssl"):
        rows.append({"name": name, "status": "passed" if shutil.which(name) else "blocked", "detail": shutil.which(name) or "not installed"})
    space=shutil.disk_usage(root)
    rows.append({'name':'testlab disk headroom','status':'passed' if space.free>=8*(1<<30) else 'blocked','detail':f'{space.free/(1<<30):.2f} GiB free on the local data filesystem; 8 GiB preparation floor, actual soak growth is measured'})
    try:
        info = json.loads(_cmd(["docker", "info", "--format", "{{json .}}"], timeout=15))
        rows.append({"name": "docker", "status": "passed", "detail": f"{info['NCPU']} CPUs / {info['MemTotal']//(1<<20)} MiB"})
    except Exception as e:
        rows.append({"name": "docker daemon", "status": "blocked", "detail": str(e)})
    import components
    ports={PORT,18560,18561,18562,18564,18565,18567,18580,18581,18582,18583,18585,18586,18587,18588,18589,18590,18591,18592,18594,18595,18596,18597,18598,18599,18600,18602,18603,18604,465,*components.PORTS.values()}
    owned_ports={};verified=False
    state=root/'.local/testlab/environment.json'
    try:
        if not state.exists():raise ValueError('no owned environment record; run prepare before claiming isolation')
        data=json.loads(state.read_text());work=_guard(root,data);owner=data['owner']
        pg=_inspect(data['container']);_owner(pg,owner)
        if pg is None or not pg.get('State',{}).get('Running'):raise ValueError('owned PostgreSQL is absent or stopped')
        if data.get('status') in ('creating','failed'):raise ValueError('owned environment preparation is incomplete')
        expected={'TEST_DATABASE_URL':'aiops_testlab','TESTLAB_MFA_DSN':'wan124mfa','TESTLAB_PASSKEY_DSN':'wan124passkey','TESTLAB_FORMAL_DSN':'wan124test','TESTLAB_APP_DSN':'aiops_testlab_app'}
        for key,database in expected.items():
            u=urllib.parse.urlparse(data.get('env',{}).get(key,''))
            if u.scheme not in ('postgres','postgresql') or u.hostname!='127.0.0.1' or u.port!=PORT or u.path!='/'+database:raise ValueError('specialized database fixture does not match dedicated loopback contract')
            extensions=_cmd(['docker','exec',data['container'],'psql','-U','testlab','-d',database,'-At','-c',"SELECT extname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE n.nspname='public' AND extname IN('vector','pgcrypto') ORDER BY extname"],timeout=15)
            if extensions.splitlines()!=['pgcrypto','vector']:raise ValueError('specialized test database lacks public vector/pgcrypto')
        rows.append({'name':'specialized databases','status':'passed','detail':'five named disposable databases; public vector/pgcrypto verified'})
        instance=json.loads((work/'instance.json').read_text())
        if instance.get('tier')!='nonprod' or {e['id'] for e in instance.get('environments',[])}!={'test','staging'}:raise ValueError('instance configuration does not match nonprod test/staging fixture')
        rows.append({'name':'instance configuration','status':'passed','detail':'explicit nonprod instance; test/staging environments'})
        heavy,_=_heavy_resources(root,owner,work)
        for name in [data['container'],*['aiops-'+owner+'-'+c for c in components.IMAGES],'aiops-'+owner+'-trial-prometheus',*heavy]:
            info=pg if name==data['container'] else _inspect(name)
            _owner(info,owner)
            if info is None:continue
            if not info.get('State',{}).get('Running'):continue
            for mappings in info.get('NetworkSettings',{}).get('Ports',{}).values():
                for mapping in mappings or []:
                    if mapping.get('HostIp')=='127.0.0.1':owned_ports[int(mapping['HostPort'])]='Docker owner label: '+name
        owned_ports.update(_process_ports(root,owner,work))
        budget=pg.get('HostConfig',{})
        rows.append({'name':'PG resources','status':'passed','detail':f"observed limit: {budget.get('Memory',0)//(1<<20)} MiB / {budget.get('NanoCpus',0)/1e9:g} CPUs; capacity suitability requires load results"})
        verified=PORT in owned_ports
    except Exception as e:
        rows.append({'name':'environment ownership/configuration','status':'blocked','detail':type(e).__name__+': '+str(e)})
    for port in sorted(ports):
        occupied=_port_open(port);proof=owned_ports.get(port)
        status='passed' if not occupied or proof else 'blocked'
        if occupied and not proof:verified=False
        rows.append({'name':'port:'+str(port),'status':status,'detail':proof if occupied and proof else 'reserved loopback port is free' if not occupied else 'occupied; testlab ownership not verified'})
    rows.append({"name": "isolation", "status": "passed" if verified else "blocked", "detail": "verified dedicated loopback DB/container/process ownership; product .env never loaded; external tenant/isolation assertions not proved" if verified else "ownership/configuration evidence incomplete; isolation cannot be claimed"})
    return rows

def prepare(root, run_id=None):
    root = Path(root).resolve()
    if run_id is not None and not re.fullmatch(r'[A-Za-z0-9_.-]+',run_id):raise ValueError('safe run-id required')
    state = root / ".local/testlab/environment.json"
    if state.exists():
        data = json.loads(state.read_text())
        _guard(root,data)
        info = _inspect(data['container'])
        _owner(info,data['owner'])
        if data.get('run_id') is None and run_id is not None:
            data['run_id']=run_id;_private(state,data)
        if info is None or data.get('status') in ('creating','failed'):
            raise RuntimeError('incomplete owned environment; cleanup its bound run-id before preparing again')
        if not info["State"]["Running"]:
            _cmd(["docker", "start", data["container"]])
        return data
    with socket.socket() as sock:
        if sock.connect_ex(("127.0.0.1", PORT)) == 0:
            raise RuntimeError(f"testlab PG port {PORT} is occupied; no existing database will be reused")
    owner = "testlab-" + uuid.uuid4().hex[:12]
    name = "aiops-" + owner + "-pg"
    password = secrets.token_hex(24)
    work = root / ".local/testlab/environments" / owner
    work.mkdir(parents=True, mode=0o700)
    postgres_env = work / "postgres.env"
    postgres_env.write_text("POSTGRES_USER=testlab\nPOSTGRES_DB=aiops_testlab\nPOSTGRES_PASSWORD=" + password + "\n")
    os.chmod(postgres_env, 0o600)
    def dsn(db): return f"postgres://testlab:{password}@127.0.0.1:{PORT}/{db}?sslmode=disable"
    data = {"owner": owner, "container": name, "work": str(work), "run_id": run_id, "status":"creating", "services": [{"name":"postgres", "mode":"real_local", "image":PG_IMAGE, "port":PORT}], "env": {"TEST_DATABASE_URL":dsn("aiops_testlab"), "TESTLAB_MFA_DSN":dsn("wan124mfa"), "TESTLAB_PASSKEY_DSN":dsn("wan124passkey"), "TESTLAB_FORMAL_DSN":dsn("wan124test"), "TESTLAB_APP_DSN":dsn("aiops_testlab_app")}}
    # Persist ownership before Docker can allocate anything. A crash/failure
    # retains a recoverable run-bound record, credentials and database files.
    _private(state, data)
    _private(work / "instance.json", {"tier":"nonprod", "name":"Backend Testlab", "domain":"testlab.local", "environments":[{"id":"test", "label":"Test", "kind":"standing", "tone":"dev", "clusters":[]}, {"id":"staging", "label":"Staging", "kind":"standing", "tone":"stg", "clusters":[]}], "default_env":"test"})
    try:
        _cmd(["docker", "run", "-d", "--name", name, "--label", f"{LABEL}={owner}", "--memory", "2g", "--cpus", "2", "--env-file", str(postgres_env), "-p", f"127.0.0.1:{PORT}:5432", "-v", str(work / "pgdata") + ":/var/lib/postgresql/data", PG_IMAGE], timeout=180)
        for _ in range(60):
            p = subprocess.run(["docker", "exec", name, "pg_isready", "-U", "testlab"], capture_output=True)
            if p.returncode == 0: break
            time.sleep(1)
        else: raise RuntimeError("dedicated PostgreSQL did not become ready")
        for db in ("wan124mfa", "wan124passkey", "wan124test", "aiops_testlab_app"):
            _cmd(["docker", "exec", name, "createdb", "-U", "testlab", db])
        for db in ("aiops_testlab", "wan124mfa", "wan124passkey", "wan124test", "aiops_testlab_app"):
            _cmd(["docker", "exec", name, "psql", "-U", "testlab", "-d", db, "-v", "ON_ERROR_STOP=1", "-c", "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;"])
    except Exception as error:
        data['status']='failed';data['failure_type']=type(error).__name__;_private(state,data)
        raise
    data['status']='ready';_private(state,data)
    return data

def cleanup(root, run_id):
    if not run_id or not re.fullmatch(r"[A-Za-z0-9_.-]+", run_id):
        raise ValueError("explicit safe run-id required")
    root=Path(root).resolve()
    state = root / ".local/testlab/environment.json"
    if not state.exists(): return {"status":"absent"}
    data = json.loads(state.read_text())
    if data.get("run_id") != run_id:
        raise ValueError("environment run-id mismatch; refusing shared or unrelated cleanup")
    work=_guard(root,data);owner=data['owner']
    from store import Store
    store=Store(root);store.reconcile_dead_runs()
    if any(r.get('status') in ('queued','running') for r in store.runs()):
        raise RuntimeError('a recorded test run is active; cleanup would interrupt its owned environment')
    if any(_port_open(port) for port in (18583,18585,18586,18587,18588,18589,18590,18591,18592,18594,18595,18596,18597,18598,18599,18600,18602,18603,18604,465)):
        raise RuntimeError('temporary HTTPS/OIDC/SMTP/Vault consumer entry is still listening; its owning invocation must finish before cleanup')
    info=_inspect(data['container']);_owner(info,owner)
    backend_path=root/'.local/testlab/backend-fixture.json'
    runtime_path=root/'.local/testlab/runtime-config.json'
    backend_data=_owned_record(backend_path,owner)
    _owned_record(runtime_path,owner)
    component_data=_owned_record(root/'.local/testlab/component-environment.json',owner)
    local_component_data=_owned_record(work/'component-environment.json',owner)
    components_present=bool(component_data or local_component_data)
    import components
    # Check every known Docker owner before stopping/removing any service.
    for component in components.IMAGES:
        component_info=_inspect('aiops-'+owner+'-'+component)
        _owner(component_info,owner)
        components_present=components_present or component_info is not None
    network=_inspect('aiops-'+owner+'-components','network');_owner(network,owner)
    components_present=components_present or network is not None
    trial_name='aiops-'+owner+'-trial-prometheus'
    trial=_inspect(trial_name);_owner(trial,owner)
    heavy,heavy_data=_heavy_resources(root,owner,work)
    heavy_info=[]
    for name in heavy:
        inspected=_inspect(name);_owner(inspected,owner)
        if inspected is not None:heavy_info.append((name,inspected))
    if backend_data:_stop_backend(backend_data)
    if info is not None or components_present:_cleanup_components(root)
    for name,_ in heavy_info:_cmd(['docker','rm','-f',name])
    if trial is not None:_cmd(['docker','rm','-f',trial_name])
    if info is not None:_cmd(["docker", "rm", "-f", data["container"]])
    # Preserve database files and reports for forensic readback. No recursive rm.
    state.unlink()
    if heavy_data:_private(work/'gitops/retained-environment.json',heavy_data)
    for path in (backend_path,runtime_path,root/'.local/testlab/gitops-environment.json'):
        if path.exists():path.unlink()
    retained_volumes=[mount.get('Name') for _,item in heavy_info for mount in item.get('Mounts',[]) if mount.get('Type')=='volume' and mount.get('Name')]
    return {"status":"cleaned", "owner":data["owner"], "retained":data["work"],"retained_docker_volumes":retained_volumes}

if __name__ == "__main__":
    import argparse
    p=argparse.ArgumentParser(); p.add_argument("command", choices=["doctor","prepare"]); p.add_argument("--root", default=str(Path(__file__).resolve().parents[2])); a=p.parse_args()
    result=doctor(a.root) if a.command=="doctor" else prepare(a.root)
    if isinstance(result,dict): result={k:v for k,v in result.items() if k!="env"}
    print(json.dumps(result, ensure_ascii=False))
