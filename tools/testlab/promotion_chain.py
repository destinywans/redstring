"""Disposable formal nonprod → reviewed GitLab → production configuration chain.

Only generated run databases/projects and held child-process handles mutate.
No business env discovery, approval SQL, production tenant, or deployment.
"""
from __future__ import annotations

import argparse
import base64
import concurrent.futures
import contextlib
from datetime import datetime, timedelta, timezone
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import ssl
import stat
import subprocess
import sys
import threading
import time
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import backend as supervisor
from env import _private, _inspect, PG_IMAGE
from faults import Blocked, attest, private_json, validate_identity
from gitops import GITLAB
from identity import (budgeted_inspect, cleanup_process, cooperative_signals, execute, free_port,
                      preserve_first_failure, remaining, safe_error)
from inventory import actual_interface
from runner import command_env, source_fingerprint

ROOT = Path(__file__).resolve().parents[2]
MODULE = 'formal-promotion'
PORTS = {'source': (18583, 18585), 'production': (18598, 18599)}
GITLAB_ORIGIN = 'https://localhost:18580'
GITLAB_PROXY_ORIGIN = 'https://localhost:18604'
GITLAB_PROXY_PORT = 18604
LIMIT = 2 << 20
LABEL = 'aiops.testlab.owner'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
GITLAB_CID = 'e348f0a8682bd85219d878864569f54111a1d3195e24bb3e8b143b407b2356a6'


def helper_hashes():
    return {name: hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('promotion_chain.py', 'api_readonly_chain.py', 'identity.py', 'gitops.py')}


@contextlib.contextmanager
def acquisition_signals():
    """Retain each newly acquired handle before honoring either cancellation."""
    interrupted = []
    previous = {sig: signal.signal(sig, lambda signum, frame: interrupted.append(signum))
                for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        yield
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    if interrupted:
        raise Blocked('promotion resource acquisition interrupted after retaining its owned handle')


def emit(name, status, actual, **fields):
    evidence_mode = fields.pop('evidence_mode', 'real_local')
    print(json.dumps(dict(id=name, name=name, status=status, actual=actual,
                          module=MODULE, evidence_mode=evidence_mode, **fields), ensure_ascii=False), flush=True)


def check(value, message):
    if not value:
        raise AssertionError(message)


def scenario_kind(scenario):
    if scenario in ('normal', 'authorization', 'boundary', 'idempotency', 'failure'):
        return scenario
    if scenario in ('concurrency', 'response-loss-retry') or scenario.startswith('upstream-') and scenario.endswith('-retry'):
        return 'idempotency'
    if scenario in ('response-loss',) or scenario.startswith('upstream-') and scenario.endswith('-response-loss'):
        return 'failure'
    return 'boundary'


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def private_bytes(path, raw):
    path = Path(path)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def owned_file(path, parent):
    path, parent = Path(path), Path(parent).resolve()
    if path.parent.resolve() != parent or path.is_symlink():
        raise Blocked('generated credential path ownership mismatch')
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or not 0 < info.st_size <= LIMIT:
        raise Blocked('generated private regular credential required')
    return path


def guard_pg(info, backend):
    expected = 'aiops-' + backend['owner'] + '-pg'
    if (not info or info.get('Name') != '/' + expected or backend.get('postgres_container') != expected
            or info.get('Config', {}).get('Image') != PG_IMAGE
            or info.get('Config', {}).get('Labels', {}).get(LABEL) != backend['owner']
            or backend.get('promotion_pg_cid') and info.get('Id') != backend['promotion_pg_cid']
            or not info.get('State', {}).get('Running')
            or info.get('NetworkSettings', {}).get('Ports', {}).get('5432/tcp') !=
            [{'HostIp': '127.0.0.1', 'HostPort': '15461'}]):
        raise Blocked('promotion PG owner/name/image/loopback port tuple mismatch')
    return info


def guard_gitlab(info, backend, config, root):
    work = Path(root).resolve() / '.local/testlab/environments' / backend['owner'] / 'gitops'
    expected = 'aiops-' + backend['owner'] + '-gitlab'
    if (config.get('owner') != backend['owner'] or Path(config.get('work', '')).resolve() != work
            or config.get('gitlab_name') != expected or config.get('gitlab_url') != GITLAB_ORIGIN
            or config.get('gitlab_image') != GITLAB or not info or info.get('Name') != '/' + expected
            or info.get('Config', {}).get('Image') != GITLAB
            or info.get('Config', {}).get('Labels', {}).get(LABEL) != backend['owner']
            or config.get('promotion_cid') and info.get('Id') != config['promotion_cid']
            or not info.get('State', {}).get('Running')
            or info.get('NetworkSettings', {}).get('Ports', {}).get('443/tcp') !=
            [{'HostIp': '127.0.0.1', 'HostPort': '18580'}]):
        raise Blocked('promotion GitLab owner/name/image/loopback tuple mismatch')
    if config.get('gitlab_user_ids') != {'root': 1, 'proposer': 2, 'reviewer': 3}:
        raise Blocked('dedicated synthetic GitLab account tuple mismatch')
    owned_file(config['ca_file'], work)
    return info


def transport(origin, method, path, body=None, headers=None, ca_file=None, timeout=15):
    allowed = [GITLAB_ORIGIN, GITLAB_PROXY_ORIGIN, *(f'https://localhost:{tls}' for tls, _ in PORTS.values())]
    if origin not in allowed or not path.startswith('/') or path.startswith('//') or '#' in path or '://' in path:
        raise Blocked('promotion request outside fixed owned origin/path')
    parsed = urllib.parse.urlsplit(origin)
    conn = http.client.HTTPSConnection(parsed.hostname, parsed.port, timeout=timeout,
                                       context=ssl.create_default_context(cafile=str(ca_file)))
    try:
        conn.request(method, path, body, headers or {})
        response = conn.getresponse()
        raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise Blocked('promotion response exceeds bounded evidence limit')
        return response.status, raw, dict(response.getheaders())
    finally:
        conn.close()


def safe_policy(branch, policy, rules, target):
    def safe(rule):
        push, merge = rule.get('push_access_levels', []), rule.get('merge_access_levels', [])
        no_named = lambda level: all(level.get(k) is None for k in ('user_id', 'group_id', 'deploy_key_id', 'member_role_id'))
        return (rule.get('allow_force_push') is False and len(push) == 1
                and push[0].get('access_level') == 0 and no_named(push[0]) and bool(merge)
                and all(item.get('access_level') == 40 and no_named(item) for item in merge))
    check(branch.get('name') == target and branch.get('protected') is True, 'exact target is not protected')
    check(policy.get('name') == target and safe(policy), 'exact protected policy is permissive')
    check(isinstance(rules, list) and len(rules) < 100, 'protected policy inventory is incomplete')
    check(any(rule.get('name') == target for rule in rules), 'exact policy missing from complete list')
    for rule in rules:
        pattern = rule.get('name', '')
        check(pattern and not any(c in pattern for c in '?[]\\'), 'unknown policy wildcard syntax')
        matches = re.fullmatch('.*'.join(re.escape(part) for part in pattern.split('*')), target)
        if matches:
            check(safe(rule), 'matching protected policy is permissive')
    return {'branch': {'name': branch['name'], 'protected': branch['protected']}, 'exact': policy, 'matching_rules': rules}


def timestamp(value):
    try:
        return datetime.fromisoformat(value.replace('Z', '+00:00'))
    except (ValueError, AttributeError):
        raise AssertionError('GitLab review time evidence missing') from None


def review_evidence(mr, versions, approvals, reviewer):
    check(versions and len(versions) < 100, 'diff version evidence incomplete')
    latest = max(versions, key=lambda item: timestamp(item.get('created_at')))
    check(latest.get('state') == 'collected' and latest.get('head_commit_sha') == mr.get('sha'),
          'review does not cover exact collected latest SHA')
    check(reviewer.get('id') == 3 and reviewer.get('bot') is False and reviewer.get('state') == 'active'
          and mr.get('author', {}).get('id') == 2, 'reviewer is not independent active synthetic human')
    approved = [item for item in approvals.get('approved_by', []) if item.get('user', {}).get('id') == 3]
    check(approvals.get('approved') is True and len(approved) == 1, 'independent exact-SHA approval not recorded')
    version_time, approval_time = timestamp(latest['created_at']), timestamp(approved[0].get('approved_at'))
    check(version_time < approval_time, 'approval preceded latest collected diff')
    if mr.get('state') == 'merged':
        check(approval_time < timestamp(mr.get('merged_at')), 'approval is not before merge')
    return {'latest_diff': latest, 'approvals': approvals, 'reviewer': reviewer,
            'reviewed_sha': mr['sha'], 'mr_iid': mr['iid']}


class TLSProxy:
    """Response drop is armed for one exact request after upstream HTTP 200."""
    def __init__(self, chain, tier):
        self.chain, self.tier = chain, tier
        self.drop = None
        self.drop_receipt = None
        self.lock = threading.Lock()
        proxy = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def relay(self):
                conn = None
                try:
                    proc = proxy.chain.processes.get(proxy.tier)
                    if proc is None or proc.poll() is not None:
                        self.send_error(503); return
                    if not self.path.startswith('/api/v1/'):
                        self.send_error(404); return
                    self.connection.settimeout(5)
                    length = int(self.headers.get('Content-Length', '0'))
                    if not 0 <= length <= 1 << 20:
                        self.send_error(413); return
                    raw = self.rfile.read(length) if length else None
                    headers = {k: v for k, v in self.headers.items() if k.lower() not in
                               ('host', 'connection', 'content-length', 'transfer-encoding', 'x-forwarded-for')}
                    headers['X-Forwarded-For'] = '127.0.0.1'
                    conn = http.client.HTTPConnection('127.0.0.1', PORTS[proxy.tier][1], timeout=15)
                    conn.request(self.command, self.path, raw, headers)
                    response = conn.getresponse(); body = response.read(LIMIT + 1)
                    if len(body) > LIMIT:
                        raise ValueError('response bound')
                    with proxy.lock:
                        dropping = proxy.drop == (self.command, self.path) and response.status == 200
                        if dropping:
                            proxy.drop = None
                            proxy.drop_receipt = {'method': self.command, 'path': self.path, 'upstream_status': 200,
                                                  'response_sha256': hashlib.sha256(body).hexdigest()}
                    if dropping:
                        self.connection.shutdown(socket.SHUT_RDWR)
                        self.connection.close(); return
                    self.send_response(response.status)
                    for key, value in response.getheaders():
                        if key.lower() not in ('connection', 'transfer-encoding', 'content-length', 'server', 'date'):
                            self.send_header(key, value)
                    self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
                except (OSError, ValueError, http.client.HTTPException):
                    try:
                        self.send_error(502)
                    except OSError:
                        pass
                finally:
                    if conn:
                        conn.close()
            do_GET = do_POST = relay
        self.server, self.thread = None, None
        try:
            with acquisition_signals():
                self.server = ThreadingHTTPServer(('127.0.0.1', PORTS[tier][0]), Handler)
                chain.proxies[tier] = self
            self.server.daemon_threads = True
            ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            ctx.load_cert_chain(str(chain.work/'entry.crt'), str(chain.work/'entry.key'))
            self.server.socket = ctx.wrap_socket(self.server.socket, server_side=True)
            self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
            with acquisition_signals():
                self.thread.start()
        except BaseException:
            self.close()
            raise

    def arm_drop(self, method, path):
        if method != 'POST' or not re.fullmatch(r'/api/v1/config-sync/mr-[1-9][0-9]*/import', path):
            raise Blocked('response drop must match one exact owned import operation')
        with self.lock:
            if self.drop is not None:
                raise Blocked('response drop already armed')
            self.drop = (method, path); self.drop_receipt = None

    def close(self):
        if self.server is not None:
            if self.thread is not None and self.thread.is_alive():
                self.server.shutdown()
            self.server.server_close()
        if self.thread is not None and self.thread.ident is not None:
            self.thread.join(timeout=2)
            check(not self.thread.is_alive(), 'owned TLS thread did not stop')


class ControlledGitLabProxy:
    """Real GitLab forwarding plus exact, recorded local protocol faults.

    The configured API authority is localhost:18604. GitLab still returns its
    canonical web_url authority localhost:18580, so the transparent control
    rewrites only that JSON URL authority. Manifest bytes and MR identity stay
    untouched until a specifically armed fault. No organization account, Go
    policy or external service is changed.
    """
    def __init__(self, chain):
        self.chain = chain
        self.lock = threading.Lock()
        self.fault = None
        self.receipts = []
        self.events = []
        self.errors = []
        self.writer = owned_file(chain.work/'source-token', chain.work).read_text().strip()
        self.reader = owned_file(chain.work/'production-token', chain.work).read_text().strip()
        self.project_id = chain.projects['main']['id']
        if not self.writer or not self.reader or self.writer == self.reader:
            raise Blocked('controlled GitLab proxy requires distinct exact generated write/read tokens')
        chain.assert_project('main')
        free_port(GITLAB_PROXY_PORT)
        proxy = self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def relay(self):
                try:
                    self.connection.settimeout(5)
                    length = int(self.headers.get('Content-Length', '0'))
                    if not 0 <= length <= 1 << 20:
                        raise Blocked('controlled GitLab request exceeds body bound')
                    body = self.rfile.read(length) if length else None
                    token = self.headers.get('PRIVATE-TOKEN')
                    role, details = proxy.guard_request(self.command, self.path, body, token)
                    proxy.chain.frozen()
                    guard_gitlab(_inspect(proxy.chain.gitlab['gitlab_name']), proxy.chain.backend,
                                 proxy.chain.gitlab, proxy.chain.root)
                    headers = {'PRIVATE-TOKEN': token, 'Content-Type': 'application/json', 'Accept-Encoding': 'identity'}
                    status, raw, upstream_headers = transport(GITLAB_ORIGIN, self.command, self.path,
                                                              body, headers, proxy.chain.gitlab['ca_file'])
                    outgoing, dropping = proxy.process_response(self.command, self.path, details, status, raw, role)
                    if dropping:
                        self.connection.shutdown(socket.SHUT_RDWR); self.connection.close(); return
                    self.send_response(status)
                    for key, value in upstream_headers.items():
                        if key.lower() not in ('connection', 'transfer-encoding', 'content-length', 'server', 'date', 'content-encoding'):
                            self.send_header(key, value)
                    self.send_header('Content-Length', str(len(outgoing))); self.end_headers(); self.wfile.write(outgoing)
                except (Blocked, AssertionError, OSError, ValueError, http.client.HTTPException) as error:
                    with proxy.lock:
                        proxy.errors.append({'error_type': type(error).__name__, 'error': safe_error(error)})
                        proxy.save()
                    try:
                        self.send_error(502)
                    except OSError:
                        pass
            do_GET = do_POST = relay
        self.server, self.thread = None, None
        try:
            with acquisition_signals():
                self.server = ThreadingHTTPServer(('127.0.0.1', GITLAB_PROXY_PORT), Handler)
                chain.git_proxy = self
            self.server.daemon_threads = True
            ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            ctx.load_cert_chain(str(chain.work/'entry.crt'), str(chain.work/'entry.key'))
            self.server.socket = ctx.wrap_socket(self.server.socket, server_side=True)
            self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
            with acquisition_signals():
                self.thread.start()
            self.save()
        except BaseException:
            self.close()
            raise

    def save(self):
        _private(self.chain.work/'controlled-gitlab-proxy.json',
                 {'owner': self.chain.backend['owner'], 'run_id': self.chain.run_id, 'origin': GITLAB_PROXY_ORIGIN,
                  'fixed_upstream': GITLAB_ORIGIN, 'project_id': self.project_id, 'events': self.events,
                  'fault_receipts': self.receipts, 'errors': self.errors,
                  'authority_mapping': {'only_json_web_url': [GITLAB_ORIGIN, GITLAB_PROXY_ORIGIN]},
                  'classification': 'controlled_protocol_fault_over_real_owned_gitlab'})

    def guard_request(self, method, path, body, token):
        if token not in (self.writer, self.reader):
            raise Blocked('controlled GitLab proxy token is outside exact generated pair')
        role = 'writer' if token == self.writer else 'reader'
        parsed = urllib.parse.urlsplit(path)
        if not path.startswith('/api/v4/') or parsed.scheme or parsed.netloc or parsed.fragment or '\\' in path:
            raise Blocked('controlled GitLab proxy rejects nonlocal request path')
        root = '/api/v4/projects/'+str(self.project_id)
        if not parsed.path.startswith(root+'/'):
            if method == 'GET' and parsed.path == '/api/v4/users/3' and not body:
                return role, {}
            raise Blocked('controlled GitLab request left exact run project/reviewer')
        if method == 'GET' and not body:
            if not any(parsed.path.startswith(root+suffix) for suffix in
                       ('/merge_requests', '/repository/branches', '/repository/files', '/repository/commits', '/protected_branches')):
                raise Blocked('controlled GitLab read endpoint is not used by configuration sync')
            return role, {}
        if method != 'POST' or role != 'writer':
            raise Blocked('controlled GitLab proxy permits writes only from exact source writer')
        query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
        if parsed.path == root+'/repository/branches':
            if body or set(query) != {'branch', 'ref'} or any(len(value) != 1 for value in query.values()):
                raise Blocked('controlled branch mutation is not exact product query shape')
            branch, ref = query['branch'][0], query['ref'][0]
            if not re.fullmatch(re.escape(self.chain.prefix)+'[a-f0-9]{32}', branch) or ref != self.chain.target:
                raise Blocked('controlled branch mutation escaped run prefix/target')
            return role, {'stage': 'branch', 'branch': branch, 'target': ref}
        if parsed.query:
            raise Blocked('controlled GitLab body write cannot include a query')
        value = json.loads(body or b'{}')
        if not isinstance(value, dict):
            raise Blocked('controlled GitLab write requires JSON object')
        if parsed.path == root+'/merge_requests':
            if set(value) != {'source_branch', 'target_branch', 'title', 'labels'}:
                raise Blocked('controlled MR write shape differs from product export')
            branch = value['source_branch']
            if not re.fullmatch(re.escape(self.chain.prefix)+'[a-f0-9]{32}', branch) or value['target_branch'] != self.chain.target or value['labels'] != 'aiops-config-promotion':
                raise Blocked('controlled MR write escaped owned branch/target/label')
            return role, {'stage': 'merge_request', 'branch': branch, 'target': value['target_branch']}
        files = root+'/repository/files/'
        if parsed.path.startswith(files):
            if set(value) != {'branch', 'content', 'commit_message'}:
                raise Blocked('controlled file write shape differs from product export')
            branch = value['branch']
            promotion = branch.removeprefix(self.chain.prefix)
            path_value = urllib.parse.unquote(parsed.path[len(files):])
            if not re.fullmatch('[a-f0-9]{32}', promotion) or branch != self.chain.prefix+promotion or path_value != 'ops-config/templates/'+promotion+'.json':
                raise Blocked('controlled file write escaped exact signed template path')
            manifest = json.loads(value['content'])
            if manifest.get('promotion_id') != promotion or manifest.get('source_instance') != self.chain.source_id or manifest.get('kind') != 'template':
                raise Blocked('controlled file write source/promotion tuple mismatch')
            return role, {'stage': 'file', 'branch': branch, 'path': path_value, 'source_id': manifest.get('source_id')}
        raise Blocked('controlled GitLab mutation endpoint is not part of export')

    def arm_drop(self, stage, template_id):
        if stage not in ('branch', 'merge_request') or not re.fullmatch('CT-[a-f0-9]{32}', template_id):
            raise Blocked('controlled upstream drop must bind one formal template and supported export stage')
        with self.lock:
            if self.fault is not None:
                raise Blocked('controlled GitLab fault already armed')
            self.fault = {'kind': 'drop', 'stage': stage, 'source_id': template_id}
            self.receipts = []; self.events = []; self.errors = []; self.save()

    def arm_cross_project(self, iid, foreign_project):
        if type(iid) is not int or iid <= 0 or type(foreign_project) is not int or foreign_project <= 0 or foreign_project == self.project_id:
            raise Blocked('cross-project fault requires exact positive IID and distinct owned project')
        foreign_keys = [key for key, value in self.chain.projects.items() if value['id'] == foreign_project]
        if len(foreign_keys) != 1:
            raise Blocked('cross-project fault identity must be another fresh owned run project')
        self.chain.assert_project(foreign_keys[0])
        with self.lock:
            if self.fault is not None:
                raise Blocked('controlled GitLab fault already armed')
            self.fault = {'kind': 'cross_project', 'iid': iid, 'foreign_project': foreign_project}
            self.receipts = []; self.events = []; self.errors = []; self.save()

    def clear(self):
        with self.lock:
            self.fault = None; self.save()

    def normalize_urls(self, value):
        if isinstance(value, list):
            return [self.normalize_urls(item) for item in value]
        if isinstance(value, dict):
            return {key: (GITLAB_PROXY_ORIGIN+item[len(GITLAB_ORIGIN):]
                          if key == 'web_url' and isinstance(item, str) and item.startswith(GITLAB_ORIGIN+'/')
                          else self.normalize_urls(item)) for key, item in value.items()}
        return value

    def process_response(self, method, path, details, status, raw, role):
        """Fault only a proven upstream success; never synthesize acceptance."""
        try:
            original = json.loads(raw)
        except ValueError:
            original = None
        with self.lock:
            event = {'method': method, 'path': path.split('?')[0], 'role': role, 'upstream_status': status,
                     'response_sha256': hashlib.sha256(raw).hexdigest(), **details}
            self.events.append(event)
            # Immutable signed file bytes are reconciled byte-for-byte by the
            # product. A raw-file response must not be parsed/reserialized.
            raw_file = urllib.parse.urlsplit(path).path.endswith('/raw')
            outgoing = self.normalize_urls(original) if original is not None and not raw_file else None
            dropping = False
            fault = self.fault
            if fault and fault['kind'] == 'drop' and method == 'POST' and role == 'writer' and details.get('stage') == fault['stage'] and status == 201:
                if not isinstance(original, dict):
                    raise AssertionError('accepted GitLab write returned non-object receipt')
                if fault['stage'] == 'merge_request':
                    check(any(item.get('stage') == 'file' and item.get('upstream_status') == 201
                              and item.get('branch') == details['branch'] and item.get('source_id') == fault['source_id']
                              for item in self.events), 'MR response drop did not follow this exact formal template write')
                receipt = event | {'fault': 'drop_after_real_gitlab_acceptance', 'source_id': fault['source_id'],
                                   'resource': original, 'owner': self.chain.backend['owner'], 'run_id': self.chain.run_id}
                self.receipts.append(receipt)
                self.fault = None; dropping = True
            elif fault and fault['kind'] == 'cross_project' and method == 'GET' and role == 'reader' and status == 200:
                root = '/api/v4/projects/'+str(self.project_id)+'/merge_requests'
                endpoint = urllib.parse.urlsplit(path).path
                targets = ([outgoing] if endpoint == root+'/'+str(fault['iid']) and isinstance(outgoing, dict)
                           else outgoing if endpoint == root and isinstance(outgoing, list) else [])
                matching = [item for item in targets if isinstance(item, dict) and item.get('iid') == fault['iid']]
                if matching:
                    check(len(matching) == 1 and matching[0].get('source_project_id') == self.project_id
                          and matching[0].get('target_project_id') == self.project_id and matching[0].get('state') == 'merged',
                          'cross-project fault must start from one real same-project merged MR')
                    target = matching[0]
                    receipt = event | {'fault': 'cross_project_schema', 'iid': target['iid'],
                            'original_source_project': target['source_project_id'], 'original_target_project': target['target_project_id'],
                            'original_sha': target.get('sha'), 'original_merge_sha': target.get('merge_commit_sha'),
                            'forged_source_project': fault['foreign_project'], 'forged_target_project': fault['foreign_project']}
                    target['source_project_id'] = target['target_project_id'] = fault['foreign_project']
                    self.receipts.append(receipt)
            self.save()
        if outgoing is None:
            return raw, dropping
        return json.dumps(outgoing, ensure_ascii=False, separators=(',', ':')).encode(), dropping

    def close(self):
        TLSProxy.close(self)


class Chain:
    def __init__(self, root, backend, gitlab, run_id):
        if not re.fullmatch('[a-f0-9]{32}', run_id):
            raise Blocked('promotion run requires a fresh 32-hex identifier')
        self.root, self.backend, self.gitlab, self.run_id = Path(root).resolve(), backend, gitlab, run_id
        if not re.fullmatch(r'testlab-[a-f0-9]{12}', backend.get('owner', '')):
            raise Blocked('promotion requires exact generated backend owner')
        expected_binary = self.root/'.local/testlab/environments'/backend['owner']/'backend/aiops'
        if Path(backend.get('binary', '')).resolve() != expected_binary or expected_binary.is_symlink():
            raise Blocked('promotion verified binary path ownership mismatch')
        self.work = self.root/'.local/testlab/config-gitops-parallel'/run_id
        if self.work.resolve() != self.work:
            raise Blocked('promotion private work traverses a symlink')
        self.work.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.module = MODULE
        self.processes, self.proxies, self.cookies = {}, {}, {}
        self.git_proxy = None
        self.databases = {tier: 'aiops_testlab_promotion_'+('prod' if tier == 'production' else 'source')+'_'+run_id for tier in PORTS}
        self.db_users = {tier: 'promotion_'+('prod' if tier == 'production' else 'source')+'_'+run_id for tier in PORTS}
        self.db_passwords = {tier: secrets.token_urlsafe(32) for tier in PORTS}
        self.created_dbs, self.pending_dbs, self.created_roles, self.projects, self.tokens = [], [], [], {}, []
        self.helper_sha256 = helper_hashes()
        runtime = self.root/'.local/testlab/runtime-config.json'
        self.runtime_sha256 = hashlib.sha256(runtime.read_bytes()).hexdigest() if runtime.is_file() else None
        self.project_name = 'testlab-promotion-' + run_id
        self.target, self.prefix = 'config-' + run_id[:12], 'aiops-promotions-' + run_id[:12] + '/'
        self.source_id = 'testlab-source-' + run_id[:12]
        self.calls, self.call_lock = 0, threading.Lock()
        self.image_hash = hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()
        self.completed = False
        self.admin = {tier: {'email': 'promotion-'+tier+'-'+run_id+'@testlab.invalid',
                             'password': secrets.token_urlsafe(36)} for tier in PORTS}
        _private(self.work/'credentials.json', {'administrators': self.admin, 'database_passwords': self.db_passwords})
        self.state = {'owner': backend['owner'], 'run_id': run_id, 'work': str(self.work), 'databases': self.databases,
                      'source_tree_sha': backend['source_tree_sha'], 'binary_sha256': self.image_hash,
                      'ports': PORTS, 'projects': self.projects, 'pids': {}, 'token_ids': self.tokens,
                      'source_instance': self.source_id, 'target': self.target, 'branch_prefix': self.prefix,
                      'database_roles': self.db_users, 'helper_sha256': self.helper_sha256, 'runtime_sha256': self.runtime_sha256}
        self.save()

    def save(self):
        _private(self.work/'state.json', self.state)

    def frozen(self):
        if source_fingerprint(self.root) != self.backend['source_tree_sha'] or hashlib.sha256(Path(self.backend['binary']).read_bytes()).hexdigest() != self.image_hash:
            raise Blocked('promotion formal source/binary changed')
        if helper_hashes() != self.helper_sha256:
            raise Blocked('promotion helper source changed during invocation')
        if self.runtime_sha256 is not None and hashlib.sha256((self.root/'.local/testlab/runtime-config.json').read_bytes()).hexdigest() != self.runtime_sha256:
            raise Blocked('promotion main runtime changed during invocation')

    def command(self, argv, *, input=None, timeout=30, binary=False):
        result = subprocess.run(argv, input=input, capture_output=True, text=not binary, timeout=timeout)
        if result.returncode:
            error = result.stderr if not binary else result.stderr.decode(errors='replace')
            _private(self.work/('command-error-'+uuid.uuid4().hex+'.json'), {'stderr': error})
            raise Blocked('owned promotion command failed; private evidence retained')
        return result.stdout

    def pg(self, database, statement, *, read=False, admin=False, deadline=None):
        if database != 'postgres' and database not in self.databases.values():
            raise Blocked('promotion SQL database is outside exact run tuple')
        if database == 'postgres' and not admin:
            raise Blocked('promotion control database requires explicit lifecycle operation')
        info = guard_pg(budgeted_inspect(self.backend['postgres_container'], deadline), self.backend)
        timeout = remaining(deadline, 8 if deadline else 30)
        payload = 'BEGIN READ ONLY; '+statement+'; COMMIT;' if read else statement
        user = 'testlab' if admin else self.db_users[next(tier for tier, name in self.databases.items() if name == database)]
        return self.command(['docker', 'exec', '-i', '-e', 'PGAPPNAME=promotion-sql-'+self.run_id, self.backend['postgres_container'], 'psql', '-X', '-qAt',
                             '-v', 'ON_ERROR_STOP=1', '-U', user, '-d', database], input=payload, timeout=timeout).strip()

    def db_marker(self, tier):
        return json.dumps({'owner': self.backend['owner'], 'run_id': self.run_id, 'tier': tier}, sort_keys=True)

    def assert_database(self, tier, deadline=None):
        name = self.databases[tier]
        marker = self.pg('postgres', 'SELECT shobj_description(oid,\'pg_database\') FROM pg_database WHERE datname='+quote(name),
                         admin=True, read=True, deadline=deadline)
        if marker != self.db_marker(tier):
            raise Blocked('promotion database owner/run/tier marker mismatch; refusing mutation')

    def create_database(self, tier):
        name = self.databases[tier]
        check(re.fullmatch('aiops_testlab_promotion_(?:source|prod)_[a-f0-9]{32}', name), 'invalid run DB name')
        existing = self.pg('postgres', 'SELECT count(*) FROM pg_database WHERE datname='+quote(name), admin=True, read=True)
        if existing != '0':
            raise Blocked('fresh promotion database already exists; refusing reuse')
        user = self.db_users[tier]
        if self.pg('postgres', 'SELECT count(*) FROM pg_roles WHERE rolname='+quote(user), admin=True, read=True) != '0':
            raise Blocked('fresh promotion role already exists; refusing reuse')
        self.created_roles.append(tier); self.state['created_roles'] = self.created_roles; self.save()
        self.pg('postgres', 'BEGIN; CREATE ROLE "'+user+'" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '+quote(self.db_passwords[tier])+'; COMMENT ON ROLE "'+user+'" IS '+quote(self.db_marker('role-'+tier))+'; COMMIT;', admin=True)
        # Record intent before nontransactional CREATE: a client timeout does
        # not prove whether the server committed. Cleanup reads this exact tuple
        # and retains a DB whose marker was not successfully established.
        self.created_dbs.append(tier); self.pending_dbs.append(tier)
        self.state['created_databases'] = self.created_dbs; self.state['pending_databases'] = self.pending_dbs; self.save()
        self.pg('postgres', 'CREATE DATABASE "'+name+'" OWNER "'+user+'";', admin=True)
        self.pg('postgres', 'COMMENT ON DATABASE "'+name+'" IS '+quote(self.db_marker(tier))+';', admin=True)
        self.pg('postgres', 'REVOKE CONNECT,TEMPORARY ON DATABASE "'+name+'" FROM PUBLIC;', admin=True)
        self.assert_database(tier)
        self.pending_dbs.remove(tier); self.save()
        self.pg(name, 'CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;', admin=True)

    def git(self, method, path, body=None, *, role='root', want=200, deadline=None, with_status=False):
        info = guard_gitlab(budgeted_inspect(self.gitlab['gitlab_name'], deadline), self.backend, self.gitlab, self.root)
        if not path.startswith('/api/v4/') or any(char in path for char in ('#', '\\')):
            raise Blocked('owned GitLab path mismatch')
        if method != 'GET':
            match = re.match(r'/api/v4/projects/([0-9]+)(?:/|$)', path)
            if match:
                owned = [key for key, item in self.projects.items() if str(item['id']) == match[1]]
                if len(owned) != 1:
                    raise Blocked('GitLab mutation outside exact fresh run project')
                if method == 'DELETE' and isinstance(body, dict) and body.get('permanently_remove') is True:
                    project = self.assert_project(owned[0], deadline, deleting=True)
                    if body != {'permanently_remove': True, 'full_path': project['path_with_namespace']}:
                        raise Blocked('permanent project cleanup requires its exact marked full path')
                else:
                    self.assert_project(owned[0], deadline)
            elif not (method == 'POST' and path in ('/api/v4/projects', '/api/v4/users/3/personal_access_tokens')
                      or method == 'DELETE' and any(path == '/api/v4/personal_access_tokens/'+str(item['id']) for item in self.tokens)):
                raise Blocked('GitLab mutation outside owned lifecycle operations')
        headers = {'PRIVATE-TOKEN': self.gitlab['tokens'][role], 'Content-Type': 'application/json'}
        status, raw, response_headers = transport(GITLAB_ORIGIN, method, path,
                    None if body is None else json.dumps(body).encode(), headers, self.gitlab['ca_file'], timeout=remaining(deadline, 15))
        expected = want if isinstance(want, tuple) else (want,)
        if status not in expected:
            row = {'stage': 'owned_gitlab', 'method': method, 'path': path.split('?')[0], 'status': status, 'expected': list(expected)}
            preserve_first_failure(self.work, row)
            raise AssertionError('owned GitLab contract HTTP '+str(status)+' for '+path.split('?')[0])
        result = (json.loads(raw) if raw else {}), response_headers
        return (*result, status) if with_status else result

    def assert_project(self, key, deadline=None, *, deleting=False):
        item = self.projects[key]
        project, _ = self.git('GET', '/api/v4/projects/'+str(item['id']), deadline=deadline)
        name = item['name']
        if deleting:
            if not re.fullmatch(r'\d{4}-\d{2}-\d{2}', project.get('marked_for_deletion_on') or ''):
                raise Blocked('permanent cleanup requires this run project already marked for deletion')
            allowed = (name, name+'-deletion_scheduled-'+str(item['id']))
            if project.get('name') not in allowed or project.get('path_with_namespace') != 'root/'+project.get('name', ''):
                raise Blocked('marked project exact deletion rename/full path mismatch')
            name = project['name']
        if project.get('id') != item['id'] or project.get('name') != name or project.get('path') != name or project.get('namespace', {}).get('id') != item['namespace_id'] or project.get('description') != self.db_marker('project-'+key):
            raise Blocked('GitLab project owner/run/id/name/namespace marker mismatch; refusing mutation')
        return project

    def create_project(self, key, protected=True):
        name = self.project_name + ('' if key == 'main' else '-'+key)
        project, _ = self.git('POST', '/api/v4/projects', {'name': name, 'path': name, 'description': self.db_marker('project-'+key),
                'initialize_with_readme': True, 'default_branch': self.target, 'visibility': 'private',
                'only_allow_merge_if_all_discussions_are_resolved': True}, want=201)
        self.projects[key] = {'id': project['id'], 'name': name, 'namespace_id': project['namespace']['id']}; self.save()
        self.assert_project(key)
        pid = project['id']
        deadline = time.monotonic()+30
        while True:
            branch, _ = self.git('GET', f'/api/v4/projects/{pid}/repository/branches/{self.target}', want=(200, 404))
            if branch.get('name') == self.target:
                break
            if time.monotonic() >= deadline:
                raise Blocked('fresh configuration-only project repository initialization timeout')
            time.sleep(.5)
        for user, level in [(2, 30), (3, 40)]:
            self.git('POST', f'/api/v4/projects/{pid}/members', {'user_id': user, 'access_level': level}, want=201)
        # Automatic default protection is replaced only in this fresh owned project.
        self.git('DELETE', f'/api/v4/projects/{pid}/protected_branches/{self.target}', want=(204, 404))
        if protected:
            self.git('POST', f'/api/v4/projects/{pid}/protected_branches', {'name': self.target,
                  'push_access_level': 0, 'merge_access_level': 40, 'allow_force_push': False}, want=201)
            self.policy(key)
        return pid

    def policy(self, key='main'):
        self.assert_project(key)
        prefix = '/api/v4/projects/'+str(self.projects[key]['id'])
        branch, _ = self.git('GET', prefix+'/repository/branches/'+self.target)
        policy, _ = self.git('GET', prefix+'/protected_branches/'+self.target)
        rules, headers = self.git('GET', prefix+'/protected_branches?per_page=100&page=1')
        check(headers.get('X-Next-Page', headers.get('x-next-page', '')) == '', 'protected inventory pagination incomplete')
        return safe_policy(branch, policy, rules, self.target)

    def setup(self):
        self.frozen()
        for ports in PORTS.values():
            for port in ports:
                free_port(port)
        free_port(GITLAB_PROXY_PORT)
        guard_pg(_inspect(self.backend['postgres_container']), self.backend)
        guard_gitlab(_inspect(self.gitlab['gitlab_name']), self.backend, self.gitlab, self.root)
        _private(self.work/'resource-start.json', {'owner': self.backend['owner'], 'run': self.run_id,
                 'postgres_container': self.backend['postgres_container'], 'postgres_image': PG_IMAGE,
                 'gitlab_container': self.gitlab['gitlab_name'], 'gitlab_image': GITLAB,
                 'databases': self.databases, 'source_instance': self.source_id, 'ports': PORTS,
                 'binary_sha256': self.image_hash, 'source_tree_sha': self.backend['source_tree_sha']})
        self.command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(self.work/'entry.key'),
                      '-out', str(self.work/'entry.crt'), '-days', '2', '-subj', '/CN=localhost',
                      '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1'])
        os.chmod(self.work/'entry.key', 0o600); os.chmod(self.work/'entry.crt', 0o600)
        self.command(['openssl', 'genpkey', '-algorithm', 'ED25519', '-out', str(self.work/'signer.pem')])
        os.chmod(self.work/'signer.pem', 0o600)
        private_der = self.command(['openssl', 'pkey', '-in', str(self.work/'signer.pem'), '-outform', 'DER'], binary=True)
        public_der = self.command(['openssl', 'pkey', '-in', str(self.work/'signer.pem'), '-pubout', '-outform', 'DER'], binary=True)
        check(len(private_der) == 48 and private_der[:16] == bytes.fromhex('302e020100300506032b657004220420')
              and len(public_der) == 44 and public_der[:12] == bytes.fromhex('302a300506032b6570032100'), 'Ed25519 DER format changed')
        private_bytes(self.work/'signing-key', base64.urlsafe_b64encode(private_der[16:]+public_der[12:]).rstrip(b'='))
        private_bytes(self.work/'public-key', base64.urlsafe_b64encode(public_der[12:]).rstrip(b'='))
        self.command(['openssl', 'pkey', '-in', str(self.work/'signer.pem'), '-pubout', '-out', str(self.work/'signer-public.pem')])
        os.chmod(self.work/'signer-public.pem', 0o600)
        self.create_project('main')
        # Only production receives a dedicated read_api PAT. Existing writer and
        # reviewer identities are synthetic and their original PATs are untouched.
        token, _ = self.git('POST', '/api/v4/users/3/personal_access_tokens',
                {'name': 'promotion-read-'+self.run_id, 'scopes': ['read_api'],
                 'expires_at': (datetime.now(timezone.utc)+timedelta(days=2)).date().isoformat()}, want=201)
        self.tokens.append({'id': token['id'], 'name': token['name'], 'user_id': 3}); self.save()
        check(token.get('scopes') == ['read_api'], 'production PAT scope is not exact read_api')
        private_bytes(self.work/'production-token', token['token'].encode())
        private_bytes(self.work/'source-token', self.gitlab['tokens']['proposer'].encode())
        bundle = Path(self.gitlab['ca_file']).read_bytes()+b'\n'+(self.work/'entry.crt').read_bytes()
        private_bytes(self.work/'trusted-ca.pem', bundle)
        for tier in PORTS:
            instance = {'tier': 'production' if tier == 'production' else 'nonprod', 'name': 'Owned promotion '+tier,
                        'domain': 'promotion-'+tier+'-'+self.run_id+'.testlab.invalid',
                        'default_env': 'prod' if tier == 'production' else 'test',
                        'environments': [{'id': 'prod' if tier == 'production' else 'test', 'label': tier,
                                          'kind': 'standing', 'tone': 'prod' if tier == 'production' else 'dev', 'clusters': []}]}
            _private(self.work/(tier+'-instance.json'), instance)
            self.configure(tier)
            self.create_database(tier)
            self.start(tier)
            self.proxies[tier] = TLSProxy(self, tier)
            self.login(tier)
        emit('promotion.owned_setup', 'passed', {'run': self.run_id, 'isolated_databases': self.databases,
                 'project_id': self.projects['main']['id'], 'production_tier': 'production',
                 'production_token_scopes': ['read_api'], 'model_inference_used': False})

    def configure(self, tier, project='main', trusted_key='public-key', origin=GITLAB_ORIGIN):
        if origin not in (GITLAB_ORIGIN, GITLAB_PROXY_ORIGIN) or origin == GITLAB_PROXY_ORIGIN and (self.git_proxy is None or project != 'main'):
            raise Blocked('configuration sync proxy origin requires this held run proxy and main project')
        binding = {'base_url': origin, 'project_id': self.projects[project]['id'], 'target_branch': self.target,
                   'branch_prefix': self.prefix, 'token_file': str(self.work/(tier+'-token'))}
        config = {'tier': 'production' if tier == 'production' else 'nonprod', 'gitlab': binding}
        if tier == 'source':
            config.update(source_instance_id=self.source_id, signing_key_file=str(self.work/'signing-key'))
        else:
            config['trusted_sources'] = {self.source_id: str(self.work/trusted_key)}
        _private(self.work/(tier+'-sync.json'), config)

    def dsn(self, tier):
        parsed = urllib.parse.urlsplit(self.backend['database_url'])
        if parsed.hostname != '127.0.0.1' or parsed.port != 15461 or parsed.username != 'testlab' or parsed.path != '/aiops_testlab_app':
            raise Blocked('base generated DSN ownership mismatch')
        query = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
        query = [(key, value) for key, value in query if key != 'application_name']
        query.append(('application_name', 'promotion-api-'+tier+'-'+self.run_id))
        return urllib.parse.urlunsplit(parsed._replace(netloc=self.db_users[tier]+':'+self.db_passwords[tier]+'@127.0.0.1:15461',path='/'+self.databases[tier],query=urllib.parse.urlencode(query)))

    def start(self, tier):
        self.frozen(); self.assert_database(tier)
        if tier not in PORTS or self.processes.get(tier) is not None:
            raise Blocked('promotion start requires unused exact tier Popen handle')
        tls, internal = PORTS[tier]; free_port(internal)
        variables = {'DATABASE_URL': self.dsn(tier), 'AIOPS_V3_INSTANCE_CONFIG': str(self.work/(tier+'-instance.json')),
                     'AIOPS_V3_CONFIG_SYNC_CONFIG': str(self.work/(tier+'-sync.json')),
                     'AIOPS_ADDR': '127.0.0.1:'+str(internal), 'AIOPS_PUBLIC_ORIGIN': 'https://localhost:'+str(tls),
                     'AIOPS_SECURE_COOKIES': 'true', 'AIOPS_TRUSTED_PROXY_CIDRS': '127.0.0.1/32',
                     'AIOPS_ADMIN_EMAIL': self.admin[tier]['email'], 'AIOPS_ADMIN_PASSWORD': self.admin[tier]['password'],
                     'SSL_CERT_FILE': str(self.work/'trusted-ca.pem'), 'OPENAI_MODEL': 'testlab',
                     'OPENAI_BASE_URL': 'http://127.0.0.1:18578/v1', 'OPENAI_API_KEY': 'synthetic-testlab-key',
                     'EMBEDDING_PROVIDER': 'openai', 'EMBEDDING_MODEL': 'synthetic-testlab',
                     'EMBEDDING_BASE_URL': 'http://127.0.0.1:18578/v1', 'EMBEDDING_API_KEY': 'synthetic-testlab-key'}
        with open(self.work/(tier+'-api.log'), 'ab') as log:
            os.chmod(log.name, 0o600)
            with acquisition_signals():
                proc = subprocess.Popen([self.backend['binary']], cwd=self.root, env=command_env(variables, {}), stdout=log, stderr=log)
                self.processes[tier] = proc; self.state['pids'][tier] = proc.pid
            self.save()
        deadline = time.monotonic()+60
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                raise Blocked('formal '+tier+' process startup failed; private log retained')
            conn = http.client.HTTPConnection('127.0.0.1', internal, timeout=2)
            try:
                conn.request('GET', '/healthz'); response = conn.getresponse(); response.read()
                if response.status == 200:
                    return
            except (OSError, http.client.HTTPException):
                pass
            finally:
                conn.close()
            time.sleep(.5)
        raise Blocked('formal '+tier+' process readiness timeout')

    def stop(self, tier, deadline=None):
        result = cleanup_process(self.processes.get(tier), deadline)
        self.processes[tier] = None
        self.state['pids'][tier] = None; self.save()
        return result

    def canonical_path(self, path):
        path = path.split('?')[0]
        path = re.sub(r'/config-sync/mr-[1-9][0-9]*/import$', '/config-sync/{id}/import', path)
        path = re.sub(r'/canvases/[^/]+', '/canvases/{id}', path)
        return path

    def call(self, tier, method, path, body=None, *, want=200, scenario='normal'):
        self.frozen()
        if tier not in PORTS or not path.startswith('/api/v1/'):
            raise Blocked('promotion business boundary mismatch')
        origin = 'https://localhost:'+str(PORTS[tier][0])
        headers = {'Origin': origin, 'X-AIOps-Environment': 'prod' if tier == 'production' else 'test', 'Content-Type': 'application/json'}
        if tier in self.cookies:
            headers['Cookie'] = self.cookies[tier]
        status, raw, response_headers = transport(origin, method, path, None if body is None else json.dumps(body).encode(),
                                                   headers, self.work/'entry.crt')
        try:
            parsed = json.loads(raw) if raw else {}
        except ValueError:
            parsed = {'error': {'code': 'non_json_response'}}
        expected = want if isinstance(want, tuple) else (want,)
        matched = status in expected and isinstance(parsed, dict) and parsed.get('error', {}).get('code') != 'non_json_response'
        row = {'method': method, 'path': self.canonical_path(path), 'scenario': scenario_kind(scenario),
               'status': 'passed' if matched else 'failed', 'expected': {'status': list(expected)},
               'actual': {'status': status, 'error_code': parsed.get('error', {}).get('code') if isinstance(parsed, dict) else 'non_object_response', 'tier': tier, 'detail_scenario': scenario},
               'registration_evidence': matched and status not in (401, 403, 404)}
        with self.call_lock:
            self.calls += 1; number = self.calls
        _private(self.work/('http-'+str(number)+'.json'), {'method': method, 'path': path, 'status': status, 'body': parsed})
        emit('promotion.http.'+str(number), row['status'], row['actual'], interface=actual_interface(row),
             **{k: v for k, v in row.items() if k not in ('status', 'actual')})
        if not matched:
            preserve_first_failure(self.work, row)
            raise AssertionError(method+' '+row['path']+' returned '+str(status)+'; expected '+str(list(expected)))
        return parsed, response_headers

    def login(self, tier):
        result, headers = self.call(tier, 'POST', '/api/v1/auth/login', self.admin[tier])
        cookie = headers.get('Set-Cookie', '')
        check(cookie.startswith('aiops_session=') and '; Secure' in cookie, 'formal HTTPS login omitted secure session cookie')
        self.cookies[tier] = cookie.split(';', 1)[0]
        check(result.get('user', {}).get('email') == self.admin[tier]['email'] or result.get('email') == self.admin[tier]['email'], 'login did not return owned bootstrap account')

    def list_state(self, tier, iid, promotion, wanted):
        result, _ = self.call(tier, 'GET', '/api/v1/config-sync')
        check(result.get('direction') == ('import' if tier == 'production' else 'export'), 'instance direction is not tier-bound')
        identity = 'mr-'+str(iid) if tier == 'production' else 'ex-'+promotion
        found = [item for item in result.get('items', []) if item.get('id') == identity]
        check(len(found) == 1 and found[0].get('status') == wanted, 'configuration state does not match persisted lifecycle')

    def counts(self, promotion):
        self.assert_database('production')
        target = 'CTP-'+promotion
        statement = "SELECT json_build_object('target',(SELECT count(*) FROM v3_canvas_templates WHERE id="+quote(target)+"),'ledger',(SELECT count(*) FROM v3_config_imports WHERE promotion_id="+quote(promotion)+"),'audit',(SELECT count(*) FROM management_audit WHERE action='v3.config_import.completed' AND target="+quote(target)+"))"
        return json.loads(self.pg(self.databases['production'], statement, read=True))

    def make_template(self, suffix):
        canvas, _ = self.call('source', 'POST', '/api/v1/canvases', {'kind': 'plan', 'title': 'Synthetic promotion '+self.run_id+' '+suffix,
                                                                 'services': [], 'ai_draft': False}, want=201)
        check(re.fullmatch(r'CV-[A-Za-z0-9_-]+', canvas.get('id', '')), 'formal canvas identity missing')
        template, _ = self.call('source', 'POST', '/api/v1/canvas-templates', {'from_canvas': canvas['id'],
                              'name': 'Owned structure '+self.run_id+' '+suffix, 'keep': {'nodes': True}}, want=201)
        check(re.fullmatch(r'CT-[A-Za-z0-9_-]+', template.get('id', '')), 'formal sanitized template identity missing')
        return template['id']

    def export(self, suffix):
        template_id = self.make_template(suffix)
        body = {'kind': 'template', 'id': template_id}
        response, _ = self.call('source', 'POST', '/api/v1/promotions', body)
        check(re.fullmatch('![1-9][0-9]*', response.get('mr', '')), 'promotion MR identity missing')
        path = response.get('path', '')
        check(re.fullmatch(r'ops-config/templates/[a-f0-9]{32}\.json', path), 'promotion path escaped expected namespace')
        promotion, iid = Path(path).stem, int(response['mr'][1:])
        repeat, _ = self.call('source', 'POST', '/api/v1/promotions', body, scenario='idempotency')
        check(repeat == response, 'same source digest exported a second MR')
        self.list_state('source', iid, promotion, 'exported')
        self.assert_database('source')
        raw = json.loads(self.pg(self.databases['source'], 'SELECT payload FROM v3_config_exports WHERE id='+quote(promotion), read=True))
        check(raw.get('source_id') == template_id and raw.get('source_instance') == self.source_id
              and raw.get('source_environment') == 'test' and raw.get('promotion_id') == promotion, 'signed export source tuple mismatch')
        graph = raw.get('template', {})
        check(graph.get('nodes') and all(node.get('blocks') == [] and node.get('sub') == '' and node.get('title') == '节点 '+str(i+1)
              for i, node in enumerate(graph['nodes'])), 'export graph was not sanitized')
        self.verify_signature(raw)
        return {'id': promotion, 'iid': iid, 'path': path, 'manifest': raw}

    def signed_bytes(self, manifest):
        fields = ('schema', 'promotion_id', 'kind', 'source_id', 'source_instance', 'source_environment', 'source_digest', 'template', 'signature')
        # Go's struct field order and HTML escaping are part of signed bytes;
        # the generated ASCII name avoids HTML characters, nodes have fixed keys.
        value = {key: manifest[key] for key in fields}; value['signature'] = ''
        graph = value['template']
        value['template'] = {key: graph[key] for key in ('kind', 'name', 'nodes', 'edges')}
        value['template']['nodes'] = [{key: node[key] for key in ('id', 'x', 'y', 'w', 'icon', 'tone', 'title', 'sub', 'blocks')} for node in graph['nodes']]
        value['template']['edges'] = [{key: edge[key] for key in ('from', 'to', 'tone', 'dash', 'live') if key in edge} for edge in graph['edges']]
        raw = json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()
        return raw.replace(b'<', b'\\u003c').replace(b'>', b'\\u003e').replace(b'&', b'\\u0026')

    def verify_signature(self, manifest):
        payload = self.signed_bytes(manifest)
        ordered = json.loads(payload)['template']
        template_raw = json.dumps(ordered, ensure_ascii=False, separators=(',', ':')).encode()
        template_raw = template_raw.replace(b'<', b'\\u003c').replace(b'>', b'\\u003e').replace(b'&', b'\\u0026')
        check(hashlib.sha256(template_raw).hexdigest() == manifest['source_digest'], 'signed template digest does not match exact graph')
        signature = base64.urlsafe_b64decode(manifest['signature']+'='*((-len(manifest['signature'])) % 4))
        check(len(signature) == 64, 'Ed25519 signature size mismatch')
        stamp = uuid.uuid4().hex
        message, sig = self.work/('signed-'+stamp), self.work/('signature-'+stamp)
        private_bytes(message, payload); private_bytes(sig, signature)
        self.command(['openssl', 'pkeyutl', '-verify', '-pubin', '-inkey', str(self.work/'signer-public.pem'),
                      '-rawin', '-in', str(message), '-sigfile', str(sig)])

    def review_merge(self, item, project='main', protected=True):
        self.assert_project(project)
        root = '/api/v4/projects/'+str(self.projects[project]['id'])+'/merge_requests/'+str(item['iid'])
        deadline = time.monotonic()+30
        while True:
            mr, _ = self.git('GET', root)
            versions, _ = self.git('GET', root+'/versions?per_page=100&page=1')
            if versions and max(versions, key=lambda v: timestamp(v.get('created_at'))).get('state') == 'collected' and mr.get('sha'):
                break
            if time.monotonic() >= deadline:
                raise Blocked('GitLab latest diff never collected')
            time.sleep(.5)
        check(mr.get('source_project_id') == self.projects[project]['id'] == mr.get('target_project_id')
              and mr.get('source_branch') == self.prefix+item['id'] and mr.get('target_branch') == self.target,
              'MR source/target tuple mismatch before review')
        before = self.policy(project) if protected else {'unprotected_synthetic_negative': True}
        # Approval binds the SHA in the POST; timestamp must strictly follow the
        # collected latest version, so wait boundedly for clocks to separate.
        latest = max(versions, key=lambda v: timestamp(v['created_at']))
        if timestamp(latest['created_at']).timestamp() >= time.time():
            time.sleep(min(2, max(.1, timestamp(latest['created_at']).timestamp()-time.time()+.1)))
        self.git('POST', root+'/approve', {'sha': mr['sha']}, role='reviewer', want=201)
        approvals, _ = self.git('GET', root+'/approvals')
        reviewer, _ = self.git('GET', '/api/v4/users/3')
        evidence = review_evidence(mr, versions, approvals, reviewer)
        _private(self.work/('review-before-'+str(item['iid'])+'-'+project+'.json'), evidence | {'policy': before})
        # GET immediately before merge verifies the reviewed head has not moved.
        current, _ = self.git('GET', root)
        check(current.get('sha') == evidence['reviewed_sha'], 'reviewed SHA moved before merge')
        before_merge = self.policy(project) if protected else before
        check(before_merge == before, 'protected policy changed between approval and merge')
        self.git('PUT', root+'/merge', {'sha': evidence['reviewed_sha'], 'should_remove_source_branch': False}, role='reviewer')
        deadline = time.monotonic()+30
        while True:
            merged, _ = self.git('GET', root)
            if merged.get('state') == 'merged':
                break
            if time.monotonic() >= deadline:
                raise Blocked('GitLab merge state not observed')
            time.sleep(.5)
        check(merged.get('sha') == evidence['reviewed_sha'] and re.fullmatch('[a-f0-9]{40}', merged.get('merge_commit_sha', '')),
              'immutable merged SHA does not match reviewed head')
        approvals, _ = self.git('GET', root+'/approvals')
        after = self.policy(project) if protected else before
        check(before == after, 'protected policy changed at merge boundary')
        evidence = review_evidence(merged, versions, approvals, reviewer)
        _private(self.work/('review-after-'+str(item['iid'])+'-'+project+'.json'), evidence | {'policy_before': before, 'policy_after': after})
        item['merge_sha'] = merged['merge_commit_sha']
        return evidence

    def verify_import(self, item):
        check(self.counts(item['id']) == {'target': 1, 'ledger': 1, 'audit': 1}, 'import target/ledger/audit is not exactly one')
        self.assert_database('production')
        target = 'CTP-'+item['id']
        actual = json.loads(self.pg(self.databases['production'],
                'SELECT row_to_json(t) FROM (SELECT kind,name,description,nodes,edges,owner_id,environment_id,is_default FROM v3_canvas_templates WHERE id='+quote(target)+') t', read=True))
        expected = item['manifest']['template'] | {'description': '', 'owner_id': None, 'environment_id': None, 'is_default': False}
        check(actual == expected, 'production graph/NULL ownership differs from signed manifest')
        ledger = json.loads(self.pg(self.databases['production'],
                'SELECT row_to_json(t) FROM (SELECT project_id,mr_iid,merge_sha,promotion_id,kind,target_id FROM v3_config_imports WHERE promotion_id='+quote(item['id'])+') t', read=True))
        check(ledger == {'project_id': self.projects['main']['id'], 'mr_iid': item['iid'], 'merge_sha': item['merge_sha'],
                         'promotion_id': item['id'], 'kind': 'template', 'target_id': target}, 'import ledger immutable provenance mismatch')
        audit = json.loads(self.pg(self.databases['production'],
                "SELECT changes FROM management_audit WHERE action='v3.config_import.completed' AND target="+quote(target), read=True))
        check(audit == {'environment_id': 'prod', 'kind': 'template', 'project_id': ledger['project_id'],
                        'mr_iid': item['iid'], 'merge_sha': item['merge_sha'], 'promotion_id': item['id']}, 'import audit exact provenance mismatch')
        self.list_state('production', item['iid'], item['id'], 'imported')

    def import_item(self, item, scenario='normal'):
        value, _ = self.call('production', 'POST', '/api/v1/config-sync/mr-'+str(item['iid'])+'/import', scenario=scenario)
        check(value.get('id') == 'mr-'+str(item['iid']) and value.get('status') == 'imported', 'formal import response not imported')
        return value

    def negative_mr(self, original, scenario, project='main'):
        self.assert_project(project)
        manifest = json.loads(json.dumps(original['manifest']))
        promotion = uuid.uuid4().hex; manifest['promotion_id'] = promotion
        if scenario == 'bad-signature':
            manifest['signature'] = base64.urlsafe_b64encode(b'\x00'*64).rstrip(b'=').decode()
        else:
            message = self.work/('negative-message-'+promotion)
            signature = self.work/('negative-signature-'+promotion)
            private_bytes(message, self.signed_bytes(manifest))
            self.command(['openssl', 'pkeyutl', '-sign', '-inkey', str(self.work/'signer.pem'), '-rawin', '-in', str(message), '-out', str(signature)])
            os.chmod(signature, 0o600)
            manifest['signature'] = base64.urlsafe_b64encode(signature.read_bytes()).rstrip(b'=').decode()
            self.verify_signature(manifest)
        path = ('other/templates/' if scenario == 'wrong-path' else 'ops-config/templates/')+promotion+'.json'
        pid = self.projects[project]['id']; branch = self.prefix+promotion
        self.git('POST', f'/api/v4/projects/{pid}/repository/branches', {'branch': branch, 'ref': self.target}, role='proposer', want=201)
        self.git('POST', f'/api/v4/projects/{pid}/repository/files/'+urllib.parse.quote(path, safe=''),
                 {'branch': branch, 'content': json.dumps(manifest, ensure_ascii=False, separators=(',', ':')), 'commit_message': 'Owned negative '+scenario}, role='proposer', want=201)
        mr, _ = self.git('POST', f'/api/v4/projects/{pid}/merge_requests',
                {'source_branch': branch, 'target_branch': self.target, 'title': 'Owned negative '+scenario,
                 'labels': 'aiops-config-promotion'}, role='proposer', want=201)
        item = {'id': promotion, 'iid': mr['iid'], 'path': path, 'manifest': manifest}
        self.review_merge(item, project, protected=scenario != 'unprotected')
        return item

    def source_record(self, template_id):
        check(re.fullmatch('CT-[a-f0-9]{32}', template_id), 'source record must bind exact formal template ID')
        self.assert_database('source')
        statement = "SELECT row_to_json(t) FROM (SELECT id,state,branch,path,mr_iid,payload,attempt_generation,source_digest FROM v3_config_exports WHERE object_id="+quote(template_id)+" AND kind='template' AND environment_id='test' AND actor_id=(SELECT id FROM users WHERE email="+quote(self.admin['source']['email'])+")) t"
        record = json.loads(self.pg(self.databases['source'], statement, read=True))
        _private(self.work/('source-record-'+template_id+'-'+uuid.uuid4().hex+'.json'), record)
        check(re.fullmatch('[a-f0-9]{32}', record.get('id', '')) and record.get('branch') == self.prefix+record['id']
              and record.get('path') == 'ops-config/templates/'+record['id']+'.json', 'source export identity is not exact run branch/path')
        payload = record.get('payload', {})
        check(payload.get('promotion_id') == record['id'] and payload.get('source_id') == template_id
              and payload.get('source_instance') == self.source_id and payload.get('source_digest') == record['source_digest'],
              'source export digest/payload identity is inconsistent')
        self.verify_signature(payload)
        return record

    def remote_snapshot(self, record, has_file):
        """Read real upstream directly, independent of the faulting proxy."""
        self.assert_project('main')
        pid = self.projects['main']['id']; root = '/api/v4/projects/'+str(pid)
        branch, _ = self.git('GET', root+'/repository/branches/'+urllib.parse.quote(record['branch'], safe=''))
        check(branch.get('name') == record['branch'] and re.fullmatch('[a-f0-9]{40}', branch.get('commit', {}).get('id', '')),
              'accepted upstream branch identity/head is unavailable')
        query = urllib.parse.urlencode({'source_branch': record['branch'], 'target_branch': self.target,
                                       'state': 'all', 'per_page': 20, 'page': 1})
        mrs, headers = self.git('GET', root+'/merge_requests?'+query)
        evidence_file = self.work/('upstream-readback-'+uuid.uuid4().hex+'.json')
        evidence = {'branch': branch, 'mrs': mrs, 'promotion_id': record['id'],
                    'mr_next_page': headers.get('X-Next-Page', headers.get('x-next-page', ''))}
        _private(evidence_file, evidence)
        check(isinstance(mrs, list) and len(mrs) <= 1 and headers.get('X-Next-Page', headers.get('x-next-page', '')) == '',
              'real upstream accepted duplicate MRs or MR inventory is incomplete')
        for mr in mrs:
            check(mr.get('source_branch') == record['branch'] and mr.get('target_branch') == self.target
                  and mr.get('source_project_id') == pid == mr.get('target_project_id') and mr.get('author', {}).get('id') == 2,
                  'real upstream MR provenance differs from uncertain export')
        commits, headers = self.git('GET', root+'/repository/commits?'+urllib.parse.urlencode(
                {'ref_name': record['branch'], 'per_page': 100, 'page': 1}))
        evidence.update(commits=commits, commit_next_page=headers.get('X-Next-Page', headers.get('x-next-page', '')))
        _private(evidence_file, evidence)
        check(isinstance(commits, list) and 0 < len(commits) < 100 and headers.get('X-Next-Page', headers.get('x-next-page', '')) == '',
              'upstream commit inventory is incomplete')
        commit_ids = [commit['id'] for commit in commits]
        check(len(set(commit_ids)) == len(commit_ids) and commit_ids[0] == branch['commit']['id'], 'upstream commit head/count mismatch')
        file, _ = self.git('GET', root+'/repository/files/'+urllib.parse.quote(record['path'], safe='')+'?'+
                          urllib.parse.urlencode({'ref': record['branch']}), want=200 if has_file else 404)
        evidence['file'] = file; _private(evidence_file, evidence)
        content_sha = None
        if has_file:
            check(file.get('file_path') == record['path'] and file.get('encoding') == 'base64', 'upstream file metadata is not exact accepted path')
            content = base64.b64decode(file['content'], validate=True)
            check(json.loads(content) == record['payload'], 'upstream file changed the persisted signed export payload')
            content_sha = hashlib.sha256(content).hexdigest()
        return {'head': branch['commit']['id'], 'commits': commit_ids, 'mr_iids': [mr['iid'] for mr in mrs],
                'file_sha256': content_sha}

    def upstream_response_loss(self, stage):
        template_id = self.make_template('upstream-response-loss-'+stage)
        body = {'kind': 'template', 'id': template_id}
        proxy = self.git_proxy; proxy.arm_drop(stage, template_id)
        response, _ = self.call('source', 'POST', '/api/v1/promotions', body, want=503,
                                scenario='upstream-'+stage+'-response-loss')
        check(response.get('error', {}).get('code') == 'config_sync_unavailable', 'lost upstream response did not report unavailable export')
        check(len(proxy.receipts) == 1 and proxy.receipts[0]['upstream_status'] == 201 and not proxy.errors,
              'upstream response loss did not follow exactly one real GitLab acceptance')
        receipt = proxy.receipts[0]
        check(receipt['source_id'] == template_id and receipt['stage'] == stage,
              'lost upstream receipt did not bind the requested formal template/stage')
        record = self.source_record(template_id)
        check(record['state'] == 'uncertain' and record['attempt_generation'] == 1 and record['mr_iid'] is None
              and receipt['branch'] == record['branch'], 'source did not persist exact uncertain export after accepted upstream write')
        before = self.remote_snapshot(record, stage == 'merge_request')
        expected_mrs = [receipt['resource']['iid']] if stage == 'merge_request' else []
        check(before['mr_iids'] == expected_mrs, 'direct upstream observation disagrees with accepted-write receipt')
        if stage == 'merge_request':
            accepted = receipt['resource']; pid = self.projects['main']['id']
            check(accepted.get('source_project_id') == pid == accepted.get('target_project_id')
                  and accepted.get('source_branch') == record['branch'] and accepted.get('target_branch') == self.target,
                  'accepted MR receipt carries foreign source/project identity')
        else:
            check(receipt['resource'].get('name') == record['branch']
                  and receipt['resource'].get('commit', {}).get('id') == before['head'], 'accepted branch receipt disagrees with real branch readback')
        # The existing product adapter reconciles the same persisted identity.
        # A 409/503, a duplicate MR/commit or changed payload remains a real red;
        # do not substitute a synthetic ledger or relax this expected contract.
        retry, _ = self.call('source', 'POST', '/api/v1/promotions', body,
                             scenario='upstream-'+stage+'-retry')
        after_record = self.source_record(template_id)
        check(after_record['id'] == record['id'] and after_record['source_digest'] == record['source_digest']
              and after_record['payload'] == record['payload'] and after_record['state'] == 'exported'
              and after_record['attempt_generation'] == 2, 'upstream-loss retry changed reserved promotion or failed to converge')
        after = self.remote_snapshot(after_record, True)
        check(after['mr_iids'] == [after_record['mr_iid']] and retry == {'mr': '!'+str(after_record['mr_iid']), 'path': record['path']},
              'upstream-loss retry did not converge on one exact MR')
        if stage == 'merge_request':
            check(after['mr_iids'] == before['mr_iids'] and after['commits'] == before['commits']
                  and after['file_sha256'] == before['file_sha256'], 'MR response-loss retry silently created another MR or commit')
        else:
            check(after['commits'][1:] == before['commits'] and len(after['commits']) == len(before['commits'])+1,
                  'accepted-branch retry did not add exactly one signed-file commit')
        for write_stage in ('branch', 'file', 'merge_request'):
            events = [event for event in proxy.events if event.get('stage') == write_stage and event.get('method') == 'POST']
            check(len(events) == 1 and events[0]['upstream_status'] == 201, 'source retry issued duplicate or unsuccessful '+write_stage+' writes')
        repeat, _ = self.call('source', 'POST', '/api/v1/promotions', body, scenario='idempotency')
        check(repeat == retry, 'post-reconciliation repeat created a different MR')
        self.list_state('source', after_record['mr_iid'], record['id'], 'exported')
        audit = json.loads(self.pg(self.databases['source'], "SELECT json_build_object('requested',count(*) FILTER(WHERE action='v3.config_export.requested'),'unknown',count(*) FILTER(WHERE action='v3.config_export.result_unknown'),'completed',count(*) FILTER(WHERE action='v3.config_export.completed')) FROM management_audit WHERE target="+quote(record['id']), read=True))
        check(audit == {'requested': 2, 'unknown': 1, 'completed': 1}, 'source unknown/reconciled audit attempts are not exact')
        check(self.counts(record['id']) == {'target': 0, 'ledger': 0, 'audit': 0}, 'unmerged recovered export left production state')
        _private(self.work/('upstream-loss-'+stage+'.json'), {'receipt': receipt, 'before': before, 'after': after,
                    'promotion_id': record['id'], 'source_audit': audit, 'reconciled_source_attempt': 2,
                    'classification': 'controlled_protocol_fault_over_real_owned_gitlab'})
        emit('promotion.upstream_response_loss.'+stage, 'passed', {'upstream_acceptance': 201,
             'initial_source_status': 503, 'initial_source_state': 'uncertain', 'retry_status': 200,
             'one_mr': after['mr_iids'][0], 'additional_commits_after_retry': 0 if stage == 'merge_request' else 1,
             'exact_unknown_completed_audit': audit}, evidence_mode='controlled_protocol_fault')

    def adversarial_protocols(self):
        self.git_proxy = ControlledGitLabProxy(self)
        self.state['gitlab_fault_proxy'] = {'origin': GITLAB_PROXY_ORIGIN, 'upstream': GITLAB_ORIGIN,
                                          'project_id': self.projects['main']['id']}; self.save()
        self.stop('source'); self.configure('source', origin=GITLAB_PROXY_ORIGIN); self.start('source'); self.login('source')
        # First prove the transparent authority mapping succeeds before any
        # injection. Export's own repeat and source GET provide positive control.
        control = self.export('proxy-transparent-control')
        for stage in ('branch', 'merge_request'):
            self.upstream_response_loss(stage)
        # Use a fresh real valid signed/independently-reviewed merged object.
        # This object has never been imported, so a denial must leave all three
        # production rows absent rather than merely preserving an old ledger.
        self.review_merge(control)
        foreign_id = self.create_project('foreign-schema', protected=False)
        self.stop('production'); self.configure('production', origin=GITLAB_PROXY_ORIGIN)
        self.start('production'); self.login('production')
        self.list_state('production', control['iid'], control['id'], 'merged')
        check(self.counts(control['id']) == {'target': 0, 'ledger': 0, 'audit': 0}, 'cross-project control object was imported prematurely')
        self.git_proxy.arm_cross_project(control['iid'], foreign_id)
        for method, path, scenario in [('GET', '/api/v1/config-sync', 'cross-project-list-schema'),
                ('POST', '/api/v1/config-sync/mr-'+str(control['iid'])+'/import', 'cross-project-detail-schema')]:
            response, _ = self.call('production', method, path, want=503, scenario=scenario)
            check(response.get('error', {}).get('code') == 'config_source_untrusted', 'forged cross-project schema did not fail at provenance gate')
            check(self.counts(control['id']) == {'target': 0, 'ledger': 0, 'audit': 0}, 'forged cross-project schema left production rows')
        receipts = list(self.git_proxy.receipts)
        expected_paths = {'/api/v4/projects/'+str(self.projects['main']['id'])+'/merge_requests',
                          '/api/v4/projects/'+str(self.projects['main']['id'])+'/merge_requests/'+str(control['iid'])}
        check(not self.git_proxy.errors and {row['path'] for row in receipts} == expected_paths
              and all(row['upstream_status'] == 200 and row['original_merge_sha'] == control['merge_sha']
                      and row['forged_source_project'] == foreign_id == row['forged_target_project'] for row in receipts),
              'cross-project denials were not backed by exact real upstream 200 MR/list schema injections')
        _private(self.work/'cross-project-schema.json', {'receipts': receipts, 'promotion_id': control['id'],
                 'production_rows_after_denial': self.counts(control['id']), 'control_positive_before_injection': True,
                 'classification': 'controlled_protocol_fault_over_real_owned_gitlab'})
        self.git_proxy.clear()
        # Clear only the proxy fault. The same original real object must remain
        # merged/eligible, establishing that malformed project fields caused the
        # denial, rather than a missing MR, broken transport or damaged object.
        self.list_state('production', control['iid'], control['id'], 'merged')
        self.import_item(control, 'normal'); self.verify_import(control)
        emit('promotion.cross_project_schema', 'passed', {'real_upstream_status': 200, 'list_and_detail_denied': 503,
             'production_rows_during_fault': {'target': 0, 'ledger': 0, 'audit': 0},
             'same_real_object_imported_after_fault_cleared': True}, evidence_mode='controlled_protocol_fault')
        self.stop('source'); self.configure('source'); self.start('source'); self.login('source')
        self.stop('production'); self.configure('production'); self.start('production'); self.login('production')

    def run(self):
        self.setup()
        item = self.export('normal')
        self.call('production', 'POST', '/api/v1/config-sync/mr-'+str(item['iid'])+'/import', want=503, scenario='unmerged')
        check(self.counts(item['id']) == {'target': 0, 'ledger': 0, 'audit': 0}, 'unmerged import left production state')
        review = self.review_merge(item)
        self.list_state('source', item['iid'], item['id'], 'merged')
        self.list_state('production', item['iid'], item['id'], 'merged')
        self.import_item(item); self.verify_import(item)
        self.import_item(item, 'idempotency'); self.verify_import(item)
        # A fresh independent object establishes two requests race before the
        # first import, rather than only repeating an already-existing ledger.
        race_item = self.export('concurrent'); self.review_merge(race_item)
        barrier = threading.Barrier(2)
        def racing():
            barrier.wait(timeout=5)
            return self.import_item(race_item, 'concurrency')
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(racing) for _ in range(2)]
            responses = [future.result(timeout=35) for future in futures]
        check(responses[0] == responses[1], 'concurrent import responses did not converge')
        self.verify_import(race_item)
        dropped = self.export('response-loss'); self.review_merge(dropped)
        path = '/api/v1/config-sync/mr-'+str(dropped['iid'])+'/import'
        proxy = self.proxies['production']; proxy.arm_drop('POST', path)
        try:
            self.import_item(dropped, 'response-loss')
        except (OSError, http.client.HTTPException):
            pass
        else:
            raise AssertionError('armed successful import response was not lost')
        check(proxy.drop_receipt and proxy.drop_receipt['path'] == path, 'response loss occurred before proven upstream success')
        _private(self.work/'response-drop.json', proxy.drop_receipt)
        drop_actual = {'status': 200, 'phase': 'held_proxy_received_product_response',
                       'response_sha256': proxy.drop_receipt['response_sha256'], 'client_response_lost': True}
        drop_row = {'method': 'POST', 'path': '/api/v1/config-sync/{id}/import', 'scenario': 'failure', 'status': 'passed',
                    'expected': {'status': [200], 'phase': 'upstream_product_commit_before_drop'}, 'actual': drop_actual,
                    'registration_evidence': True}
        emit('promotion.response_drop', 'passed', drop_actual, interface=actual_interface(drop_row))
        self.verify_import(dropped); self.import_item(dropped, 'response-loss-retry'); self.verify_import(dropped)
        self.stop('production'); self.start('production'); self.login('production')
        for object_item in (item, race_item, dropped):
            self.verify_import(object_item)
        emit('promotion.lifecycle', 'passed', {'objects': 3, 'same_digest_same_mr': True, 'target_ledger_audit_each': 1,
                 'independent_exact_sha_review': review['reviewed_sha'], 'parallel_imports': 2,
                 'upstream_committed_response_lost': True, 'fresh_process_and_sql_connection_readback': True})
        self.adversarial_protocols()
        # Negative MRs are independent new objects after all normal list reads;
        # invalid merged items correctly make the bounded import list fail closed.
        for scenario in ('bad-signature', 'wrong-path'):
            negative = self.negative_mr(item, scenario)
            self.call('production', 'POST', '/api/v1/config-sync/mr-'+str(negative['iid'])+'/import', want=503, scenario=scenario)
            check(self.counts(negative['id']) == {'target': 0, 'ledger': 0, 'audit': 0}, scenario+' left production target/ledger/audit')
        self.create_project('unprotected', protected=False)
        negative = self.negative_mr(item, 'unprotected', project='unprotected')
        self.stop('production'); self.configure('production', 'unprotected'); self.start('production'); self.login('production')
        self.call('production', 'POST', '/api/v1/config-sync/mr-'+str(negative['iid'])+'/import', want=503, scenario='unprotected')
        check(self.counts(negative['id']) == {'target': 0, 'ledger': 0, 'audit': 0}, 'unprotected target was imported')
        # The reviewed MR exists solely in main; request a nonexisting IID in
        # the alternate fixed project. Do not claim forged cross-project schema
        # coverage from this lookup isolation observation.
        other_iid = 999999999
        self.call('production', 'POST', '/api/v1/config-sync/mr-'+str(other_iid)+'/import', want=503, scenario='wrong-project-lookup')
        emit('promotion.negative_boundaries', 'passed', {'bad_signature': 'separate real merged MR', 'wrong_path': 'separate real merged MR',
                  'unprotected_target': 'separate disposable project, valid signature and independent review', 'wrong_project': 'fixed-project lookup isolation only'})
        emit('promotion.remaining', 'external_todo', {'unverified': ['enterprise historical governance and physical reviewer identity'],
                  'classification': 'external_todo', 'local_protocol_adversarial_scenarios_completed': True,
                  'core_chain_completed': True})
        self.frozen(); self.completed = True
        return {'run': self.run_id, 'calls': self.calls, 'normal_objects': 3, 'verified_core': True,
                'local_protocol_faults': ['accepted branch response loss', 'accepted MR response loss', 'cross-project list/detail schema'],
                'remaining': ['enterprise historical governance and physical reviewer identity']}

    def cleanup(self):
        deadline = time.monotonic()+40
        stages = {}
        errors, observations = [], {'owner': self.backend['owner'], 'run_id': self.run_id, 'processes': {}, 'projects': [], 'databases': [], 'roles': [], 'tokens': []}
        def attempt(label, action):
            try:
                return action()
            except BaseException as error:
                row = {'resource': label, 'stage': stages.get(label, 'cleanup'),
                       'error_type': type(error).__name__, 'error': safe_error(error)}
                if isinstance(error, subprocess.TimeoutExpired):
                    row['command_timeout_seconds'] = error.timeout
                errors.append(row)
                return None
        for tier, proxy in list(self.proxies.items()):
            attempt('proxy-'+tier, proxy.close)
        if self.git_proxy is not None:
            attempt('controlled-gitlab-proxy', self.git_proxy.close)
        for tier in PORTS:
            observations['processes'][tier] = attempt('process-'+tier, lambda tier=tier: self.stop(tier, deadline))
        # Every resource is independently guarded before mutation. Partial setup
        # failure never authorizes base DB, old project, or old PAT cleanup.
        for key in list(self.projects):
            def delete_project(key=key):
                self.assert_project(key, deadline)
                pid = self.projects[key]['id']
                self.git('DELETE', '/api/v4/projects/'+str(pid), want=202, deadline=deadline)
                row = {'id': pid, 'name': self.projects[key]['name'], 'delete_scheduled': True, 'api_absent': False}
                observations['projects'].append(row)
                # GitLab 18+ Free uses delayed deletion. Only this generated
                # project, its exact marker and actual renamed full path qualify
                # for immediate fixture cleanup; instance settings stay intact.
                marked = self.assert_project(key, deadline, deleting=True)
                self.git('DELETE', '/api/v4/projects/'+str(pid),
                         {'permanently_remove': True, 'full_path': marked['path_with_namespace']},
                         want=202, deadline=deadline)
                row['permanent_remove_requested'] = True
            attempt('project-'+key, delete_project)
        for token in self.tokens:
            def revoke(token=token):
                actual, _ = self.git('GET', '/api/v4/personal_access_tokens/'+str(token['id']), deadline=deadline)
                if any(actual.get(key) != token[key] for key in ('id', 'name', 'user_id')) or actual.get('scopes') != ['read_api']:
                    raise Blocked('run PAT exact identity/scope mismatch; refusing revoke')
                self.git('DELETE', '/api/v4/personal_access_tokens/'+str(token['id']), want=204, deadline=deadline)
                observations['tokens'].append({'id': token['id'], 'revoked': True})
            attempt('token-'+str(token['id']), revoke)
        for tier in self.created_dbs:
            def drop_db(tier=tier):
                label = 'database-'+tier
                stages[label] = 'held-process-stop'
                if self.processes.get(tier) is not None:
                    raise Blocked('run process not confirmed stopped; refusing DB drop')
                name = self.databases[tier]
                intent = None
                if tier in self.pending_dbs:
                    stages[label] = 'creation-intent-readback'
                    intent = json.loads(self.pg('postgres',
                        "SELECT json_build_object('present', EXISTS(SELECT 1 FROM pg_database WHERE datname="+quote(name)+"),"
                        "'owner', (SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname="+quote(name)+"),"
                        "'active_create_commands', (SELECT count(*) FROM pg_stat_activity WHERE application_name="+quote('promotion-sql-'+self.run_id)+
                        " AND query="+quote('CREATE DATABASE "'+name+'" OWNER "'+self.db_users[tier]+'";')+" AND state='active'))",
                            read=True, admin=True, deadline=deadline))
                    row = {'name': name, 'creation_intent_readback': intent, 'dropped': False}
                    observations['databases'].append(row)
                    if intent.get('present') is False and intent.get('active_create_commands') == 0:
                        row.update(dropped=True, creation_not_committed_or_already_absent=True)
                        return
                    if intent.get('owner') != self.db_users[tier] or intent.get('active_create_commands') != 0:
                        raise Blocked('run DB creation outcome/owner is uncertain; retained without DDL replay')
                stages[label] = 'owner-marker'
                self.assert_database(tier, deadline)
                stages[label] = 'active-connections'
                connections = self.pg('postgres', 'SELECT count(*) FROM pg_stat_activity WHERE datname='+quote(name), read=True, admin=True, deadline=deadline)
                if connections != '0':
                    raise Blocked('run DB still has active connections; retained without forced termination')
                if intent is None:
                    row = {'name': name, 'dropped': False}
                    observations['databases'].append(row)
                row['owner_marker_verified'] = True
                statement = 'DROP DATABASE "'+name+'";'
                stages[label] = 'drop-database'
                timed_out = None
                try:
                    self.pg('postgres', statement, admin=True, deadline=deadline)
                except subprocess.TimeoutExpired as error:
                    # Killing the Docker client does not establish whether
                    # PostgreSQL committed DROP or is still waiting for its
                    # checkpoint. Never issue a second DROP on an uncertain
                    # result: confirm the exact resource through a new read.
                    timed_out = error
                    row['drop_error'] = {'stage': 'drop-database', 'error_type': 'TimeoutExpired',
                                         'command_timeout_seconds': error.timeout}
                stages[label] = 'drop-readback'
                raw = self.pg('postgres',
                        "SELECT json_build_object('present', EXISTS(SELECT 1 FROM pg_database WHERE datname="+quote(name)+"),"
                        "'owner_marker', (SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname="+quote(name)+"),"
                        "'connections', (SELECT count(*) FROM pg_stat_activity WHERE datname="+quote(name)+"),"
                        "'active_drop_commands', (SELECT count(*) FROM pg_stat_activity WHERE state='active' AND usename='testlab' AND query IN ("+
                        quote(statement)+","+quote(statement.rstrip(';'))+")))", read=True, admin=True, deadline=deadline)
                state = json.loads(raw)
                row['readback'] = state
                if (state.get('present') is False and state.get('connections') == 0
                        and state.get('active_drop_commands') == 0):
                    row['dropped'] = True
                    row['timeout_recovered'] = timed_out is not None
                    return
                if state.get('present') is not False:
                    if state.get('owner_marker') != self.db_marker(tier):
                        raise Blocked('run DB readback marker changed; refusing further mutation')
                    if timed_out is not None:
                        raise timed_out
                    raise Blocked('successful DROP still has an owned database; cleanup unconfirmed')
                # Catalog absence alone is insufficient while the exact
                # DROP session still runs. Preserve uncertainty as a red;
                # do not retry the deletion or outlive the cleanup budget.
                raise Blocked('run DB absent but connections or DROP session remain; cleanup unconfirmed')
            attempt('database-'+tier, drop_db)
        for tier in self.created_roles:
            def drop_role(tier=tier):
                user = self.db_users[tier]
                label = 'role-'+tier
                stages[label] = 'database-creation-outcome'
                if tier in self.pending_dbs and not any(row['name'] == self.databases[tier] and row['dropped']
                                                       for row in observations['databases']):
                    raise Blocked('run database creation outcome remains unconfirmed; retaining its role')
                stages[label] = 'role-marker'
                marker = self.pg('postgres', "SELECT COALESCE(shobj_description(oid,'pg_authid'),'') FROM pg_roles WHERE rolname="+quote(user), admin=True, read=True, deadline=deadline)
                if marker == '':
                    exists = self.pg('postgres', 'SELECT count(*) FROM pg_roles WHERE rolname='+quote(user), admin=True, read=True, deadline=deadline)
                    if exists == '0':
                        observations['roles'].append({'name': user, 'absent': True, 'creation_not_committed': True})
                        return
                if marker != self.db_marker('role-'+tier):
                    raise Blocked('promotion role owner/run/tier marker mismatch; refusing drop')
                stages[label] = 'role-owned-database'
                owned = self.pg('postgres', 'SELECT count(*) FROM pg_database WHERE datdba=(SELECT oid FROM pg_roles WHERE rolname='+quote(user)+')', admin=True, read=True, deadline=deadline)
                if owned != '0':
                    raise Blocked('owned role still owns a database; retained without reassigning')
                stages[label] = 'drop-role'
                # Transactional role DROP has no timeout replay. A fresh read
                # establishes outcome; a present role remains an explicit red.
                timeout_error = None
                try:
                    self.pg('postgres', 'DROP ROLE "'+user+'";', admin=True, deadline=deadline)
                except subprocess.TimeoutExpired as error:
                    timeout_error = error
                stages[label] = 'role-readback'
                absent = self.pg('postgres', 'SELECT count(*) FROM pg_roles WHERE rolname='+quote(user), admin=True, read=True, deadline=deadline) == '0'
                observations['roles'].append({'name': user, 'owner_marker_verified': True, 'absent': absent, 'timeout_recovered': timeout_error is not None and absent})
                if not absent:
                    if timeout_error is not None:
                        raise timeout_error
                    raise Blocked('owned role DROP not confirmed absent')
            attempt('role-'+tier, drop_role)
        # Wait for GitLab asynchronous deletes only after independent API/DB/
        # role cleanup, so a slow project job cannot consume their budget.
        for row in observations['projects']:
            def wait_project_absence(row=row):
                while remaining(deadline, 1) > 0:
                    result, _, status = self.git('GET', '/api/v4/projects/'+str(row['id']), want=(200, 404), deadline=deadline, with_status=True)
                    if status == 404 and result.get('message') == '404 Project Not Found':
                        row['api_absent'] = True
                        row['readback_status'] = status
                        return
                    time.sleep(.2)
                raise Blocked('owned project deletion still visible at cleanup deadline')
            attempt('project-readback-'+str(row['id']), wait_project_absence)
        observations['errors'] = errors
        observations['private_evidence_retained'] = True
        _private(self.work/'resource-end.json', observations)
        if errors:
            raise Blocked('promotion owned cleanup incomplete; separate private resource-end errors retained')
        emit('promotion.cleanup', 'passed', observations)


def doctor(root):
    backend, _ = attest(root)
    current = supervisor.attest(backend)
    validate_identity(backend, current)
    runtime = Path(root)/'.local/testlab/runtime-config.json'
    if (runtime.is_symlink() or not runtime.is_file() or not backend.get('runtime_sha')
            or hashlib.sha256(runtime.read_bytes()).hexdigest() != backend['runtime_sha']):
        raise Blocked('promotion main runtime differs from verified fixture')
    gitlab = private_json(Path(root)/'.local/testlab/gitops-environment.json')
    pg = guard_pg(_inspect(backend['postgres_container']), backend)
    gl = guard_gitlab(_inspect(gitlab['gitlab_name']), backend, gitlab, root)
    if pg.get('Id') != PG_CID or gl.get('Id') != GITLAB_CID:
        raise Blocked('promotion exact authorized PG/GitLab CID mismatch')
    backend = dict(backend, promotion_pg_cid=pg['Id'])
    gitlab = dict(gitlab, promotion_cid=gl['Id'])
    for ports in PORTS.values():
        for port in ports:
            free_port(port)
    free_port(GITLAB_PROXY_PORT)
    return backend, gitlab


@contextlib.contextmanager
def exclusive(root):
    import fcntl
    path = Path(root)/'.local/testlab/promotion.lock'
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private promotion lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('another promotion chain owns fixed local ports') from None
        yield
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=('doctor', 'chain'))
    args = parser.parse_args()
    try:
        with cooperative_signals(), exclusive(ROOT):
            backend, gitlab = doctor(ROOT)
            if args.action == 'doctor':
                emit('promotion.doctor', 'passed', {'owner': backend['owner'], 'source_tree_sha': backend['source_tree_sha'],
                     'hmac_supervisor_verified': True, 'dedicated_ports_free': PORTS, 'formal_production_required': True})
                return 0
            chain = Chain(ROOT, backend, gitlab, uuid.uuid4().hex)
            actual = execute(chain)
            emit('promotion.chain', 'passed', actual)
            return 0
    except BaseException as error:
        emit('promotion.chain', 'blocked' if isinstance(error, Blocked) else 'failed', {'error_type': type(error).__name__, 'error': safe_error(error)})
        return 2 if isinstance(error, Blocked) else 1


if __name__ == '__main__':
    raise SystemExit(main())
