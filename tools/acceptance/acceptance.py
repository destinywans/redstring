#!/usr/bin/env python3
"""Production-acceptance instances for Redstring (2026-10-09).

Two isolated instances against the aiops-acceptance-pg container:
  prod     http://localhost:18090  tier=production, env prod  -> aiops-test VM cluster
  nonprod  http://localhost:18091  tier=nonprod, envs dev/test/staging

Secrets, generated configs, logs and pid files live in .local/acceptance/ (git-ignored).
The model key is read from the repo .env (OPENAI_API_KEY, DeepSeek); nothing is printed.
Never touches the developer database, testlab data or any non-lab cluster.
"""
import argparse
import json
import os
import secrets
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/acceptance'
PG_ENV = LOCAL / 'pg.env'
PG_PORT = 25432
WEB_DIR = LOCAL / 'web'
BIN = LOCAL / 'bin/aiops'
MODELS = ['deepseek-flash', 'deepseek-v4-pro']

# Both tiers sit behind the TLS proxy: passkeys need https and a real RP ID
# (localhost/IPs are refused). Browsers map *.redstring.test to 127.0.0.1 with
# --host-resolver-rules; nothing is added to /etc/hosts or the keychain.
PROXY = 'aiops-acceptance-proxy'
TLS = LOCAL / 'tls'
# The lab Alertmanager speaks plain HTTP; a production tier only accepts HTTPS, so the proxy
# also fronts it on 127.0.0.1 with its own IP-SAN cert (trusted via ca_file) and a bearer token.
ALERTMANAGER = {'https': 18445, 'upstream': 'host.docker.internal:19093'}
INSTANCES = {
    'prod': {
        'port': 18090, 'https': 18443, 'host': 'prod.redstring.test', 'db': 'redstring_prod',
        'instance': {
            'tier': 'production', 'name': 'Redstring Production · acceptance', 'domain': 'prod.redstring.test',
            'environments': [{'id': 'prod', 'label': 'Production', 'kind': 'standing', 'tone': 'prod', 'clusters': ['aiops-vm-cluster']}],
            'default_env': 'prod',
            'other': {'label': 'Non-production', 'url': 'https://nonprod.redstring.test:18444', 'domain': 'nonprod.redstring.test'},
        },
    },
    'nonprod': {
        'port': 18091, 'https': 18444, 'host': 'nonprod.redstring.test', 'db': 'redstring_nonprod',
        'instance': {
            'tier': 'nonprod', 'name': 'Redstring Non-production · acceptance', 'domain': 'nonprod.redstring.test',
            'environments': [
                {'id': 'dev', 'label': 'Development', 'kind': 'standing', 'tone': 'dev', 'clusters': []},
                {'id': 'test', 'label': 'Test', 'kind': 'standing', 'tone': 'dev', 'clusters': ['aiops-vm-cluster']},
                {'id': 'staging', 'label': 'Staging', 'kind': 'standing', 'tone': 'stg', 'clusters': []},
            ],
            'default_env': 'test',
            'other': {'label': 'Production', 'url': 'https://prod.redstring.test:18443', 'domain': 'prod.redstring.test'},
        },
    },
}


def origin(name):
    spec = INSTANCES[name]
    return f"https://{spec['host']}:{spec['https']}"


def private_write(path: Path, data, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + '.tmp')
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
    with os.fdopen(fd, 'wb' if isinstance(data, bytes) else 'w') as f:
        f.write(data)
    os.replace(tmp, path)


def read_env(path: Path):
    out = {}
    for line in path.read_text().splitlines():
        if line and not line.startswith('#') and '=' in line:
            k, v = line.split('=', 1)
            out[k] = v
    return out


def secrets_for(name):
    """Per-instance generated secrets, created once and reused."""
    path = LOCAL / name / 'secrets.json'
    if path.exists():
        return json.loads(path.read_text())
    value = {'admin_email': f'admin@{name}.redstring.test', 'admin_password': secrets.token_urlsafe(18)}
    private_write(path, json.dumps(value, indent=2) + '\n')
    return value


def prepare(name):
    spec = INSTANCES[name]
    home = LOCAL / name
    home.mkdir(parents=True, exist_ok=True)
    os.chmod(home, 0o700)
    sec = secrets_for(name)
    key = home / 'mfa.key'
    if not key.exists():
        private_write(key, secrets.token_bytes(32))
    private_write(home / 'instance.json', json.dumps(spec['instance'], indent=2) + '\n')
    envs = [e['id'] for e in spec['instance']['environments']]
    bindings = {'tier': spec['instance']['tier'], 'environments': [
        {'id': e, 'models': [f'openai:{m}' for m in MODELS], 'default_model': f'openai:{MODELS[0]}'} for e in envs]}
    private_write(home / 'model-bindings.json', json.dumps(bindings, indent=2) + '\n')
    pg = read_env(PG_ENV)
    model = read_env(ROOT / '.env')
    env = {
        'DATABASE_URL': f"postgres://aiops:{pg['POSTGRES_PASSWORD']}@127.0.0.1:{PG_PORT}/{spec['db']}?sslmode=disable",
        'AIOPS_ADDR': f"127.0.0.1:{spec['port']}",
        'AIOPS_PUBLIC_ORIGIN': origin(name),
        # The nginx proxy reaches the server through Docker Desktop's host loopback.
        'AIOPS_TRUSTED_PROXY_CIDRS': '127.0.0.1/32',
        'AIOPS_WEB_DIR': str(WEB_DIR),
        'AIOPS_ADMIN_EMAIL': sec['admin_email'],
        'AIOPS_ADMIN_PASSWORD': sec['admin_password'],
        'AIOPS_V3_INSTANCE_CONFIG': str(home / 'instance.json'),
        'AIOPS_V3_MODEL_BINDINGS_FILE': str(home / 'model-bindings.json'),
        'AIOPS_V3_MFA_KEY_FILE': str(key),
        'AIOPS_V3_PASSKEY_ENABLED': 'true',
        'AIOPS_SECURE_COOKIES': 'true',
        'OPENAI_BASE_URL': model['OPENAI_BASE_URL'],
        'OPENAI_API_KEY': model['OPENAI_API_KEY'],
        'OPENAI_MODEL': ','.join(MODELS),
        'OPENAI_PROVIDER_NAME': 'DeepSeek',
        'OPENAI_MAX_OUTPUT_TOKENS': '4096',
        'AIOPS_INFERENCE_MAX_CALLS': '8',
        'EMBEDDING_PROVIDER': 'ollama',
        'EMBEDDING_MODEL': 'bge-m3:567m',
        'EMBEDDING_BASE_URL': 'http://127.0.0.1:21434',
    }
    extra = home / 'extra.env'  # domain configs added as acceptance progresses
    if extra.exists():
        env.update(read_env(extra))
    private_write(home / 'server.env', ''.join(f'{k}={v}\n' for k, v in env.items()))
    return env


def pid_of(name):
    p = LOCAL / name / 'server.pid'
    if not p.exists():
        return None
    pid = int(p.read_text())
    try:
        os.kill(pid, 0)
        return pid
    except OSError:
        return None


def healthy(name, timeout=60):
    url = f"http://127.0.0.1:{INSTANCES[name]['port']}/healthz"
    until = time.time() + timeout
    while time.time() < until:
        try:
            with urllib.request.urlopen(url, timeout=3) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        if pid_of(name) is None:
            return False
        time.sleep(1)
    return False


def start(name):
    if pid_of(name):
        print(f'{name}: already running')
        return 0
    env = {**{k: v for k, v in os.environ.items() if not k.startswith(('AIOPS_', 'OPENAI_', 'OLLAMA_', 'CLAUDE_', 'EMBEDDING_', 'DATABASE_URL'))}, **prepare(name)}
    log = open(LOCAL / name / 'server.log', 'ab')
    proc = subprocess.Popen([str(BIN)], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    (LOCAL / name / 'server.pid').write_text(str(proc.pid))
    ok = healthy(name)
    print(f"{name}: {'ready' if ok else 'NOT READY (see .local/acceptance/' + name + '/server.log)'} at {origin(name)}")
    return 0 if ok else 1


def stop(name):
    pid = pid_of(name)
    if not pid:
        print(f'{name}: not running')
        return
    os.killpg(os.getpgid(pid), signal.SIGTERM)
    for _ in range(30):
        if pid_of(name) is None:
            break
        time.sleep(0.5)
    print(f'{name}: stopped')


def tls():
    """Self-signed cert for *.redstring.test plus an nginx TLS proxy (SSE unbuffered)."""
    TLS.mkdir(parents=True, exist_ok=True)
    os.chmod(TLS, 0o700)
    cert, key = TLS / 'cert.pem', TLS / 'key.pem'
    if not cert.exists():
        names = ','.join(f"DNS:{spec['host']}" for spec in INSTANCES.values())
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes',
                        '-keyout', str(key), '-out', str(cert), '-days', '60', '-subj', '/CN=redstring.test acceptance',
                        '-addext', f'subjectAltName={names}'], check=True, capture_output=True)
        os.chmod(key, 0o600)
    # SPKI hash for Chromium's --ignore-certificate-errors-spki-list (trusts only this key).
    pub = subprocess.run(['openssl', 'x509', '-in', str(cert), '-pubkey', '-noout'], check=True, capture_output=True).stdout
    der = subprocess.run(['openssl', 'pkey', '-pubin', '-outform', 'der'], input=pub, check=True, capture_output=True).stdout
    import base64, hashlib
    (TLS / 'spki.txt').write_text(base64.b64encode(hashlib.sha256(der).digest()).decode() + '\n')
    am_cert, am_key, am_token = TLS / 'alertmanager.pem', TLS / 'alertmanager.key', TLS / 'alertmanager.token'
    if not am_cert.exists():
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1', '-nodes',
                        '-keyout', str(am_key), '-out', str(am_cert), '-days', '60', '-subj', '/CN=alertmanager acceptance',
                        '-addext', 'subjectAltName=IP:127.0.0.1', '-addext', 'basicConstraints=critical,CA:TRUE'],
                       check=True, capture_output=True)
        os.chmod(am_key, 0o600)
    if not am_token.exists():
        private_write(am_token, secrets.token_urlsafe(32) + '\n')
    token = am_token.read_text().strip()
    # Upstream keepalive: Docker Desktop's host.docker.internal path times out when every
    # proxied request opens a new connection (seen under 8 parallel browsers).
    upstreams = ''.join(f"""
    upstream {name} {{
        server host.docker.internal:{spec['port']};
        keepalive 64;
    }}""" for name, spec in INSTANCES.items())
    servers = ''.join(f"""
    server {{
        listen {spec['https']} ssl;
        server_name {spec['host']};
        ssl_certificate /tls/cert.pem;
        ssl_certificate_key /tls/key.pem;
        client_max_body_size 64m;
        location / {{
            proxy_pass http://{name};
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host $host:$server_port;
            proxy_set_header X-Forwarded-For $remote_addr;
            proxy_set_header X-Forwarded-Proto https;
            proxy_buffering off;
            proxy_connect_timeout 10s;
            proxy_read_timeout 3600s;
        }}
    }}""" for name, spec in INSTANCES.items())
    servers += f"""
    server {{
        listen {ALERTMANAGER['https']} ssl;
        ssl_certificate /tls/alertmanager.pem;
        ssl_certificate_key /tls/alertmanager.key;
        location / {{
            if ($http_authorization != "Bearer {token}") {{ return 401; }}
            proxy_pass http://{ALERTMANAGER['upstream']};
            proxy_set_header Authorization "";
            proxy_connect_timeout 5s;
            proxy_read_timeout 30s;
        }}
    }}"""
    conf = TLS / 'nginx.conf'
    conf.write_text('worker_processes 2;\nevents { worker_connections 4096; }\nhttp {' + upstreams + servers + '\n}\n')
    os.chmod(conf, 0o600)
    subprocess.run(['docker', 'rm', '-f', PROXY], capture_output=True)
    ports = sum((['-p', f"127.0.0.1:{p}:{p}"] for p in [*(spec['https'] for spec in INSTANCES.values()), ALERTMANAGER['https']]), [])
    subprocess.run(['docker', 'run', '-d', '--name', PROXY, '--label', 'aiops.acceptance=2026-10-09', *ports,
                    '-v', f'{TLS}:/tls:ro', '-v', f"{TLS / 'nginx.conf'}:/etc/nginx/nginx.conf:ro", 'nginx:latest'],
                   check=True, capture_output=True)
    print(f"{PROXY}: https on {', '.join(origin(n) for n in INSTANCES)}; SPKI in .local/acceptance/tls/spki.txt")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('command', choices=['prepare', 'start', 'stop', 'restart', 'status', 'tls'])
    ap.add_argument('instance', nargs='?', choices=[*INSTANCES, 'all'], default='all')
    a = ap.parse_args()
    if a.command == 'tls':
        tls()
        return
    names = list(INSTANCES) if a.instance == 'all' else [a.instance]
    rc = 0
    for n in names:
        if a.command == 'prepare':
            prepare(n)
            print(f'{n}: configuration written to .local/acceptance/{n}/')
        elif a.command == 'start':
            rc |= start(n)
        elif a.command == 'stop':
            stop(n)
        elif a.command == 'restart':
            stop(n)
            rc |= start(n)
        else:
            print(f"{n}: {'running pid ' + str(pid_of(n)) if pid_of(n) else 'stopped'}, healthy={healthy(n, 2) if pid_of(n) else False}")
    sys.exit(rc)


if __name__ == '__main__':
    main()
