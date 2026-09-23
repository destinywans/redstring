#!/usr/bin/env python3
"""Root-owned, stdin-only deployment protocol. Never accepts a shell command.

Install as /usr/local/sbin/aiops-deploy-helper. The SSH account may sudo this
single executable without arguments; target configuration must be root-owned.
An interrupted operation remains reserved until status can establish its outcome.
"""
import fcntl
import hashlib
import gzip
import json
import os
from pathlib import Path
import re
import pwd
import signal
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

CONFIG = Path('/etc/aiops-deployment/targets.json')
STATE = Path('/var/lib/aiops-deployment')
IDENT = re.compile(r'^[a-z][a-z0-9-]{0,62}$')
OP = re.compile(r'^[a-f0-9]{32}$')
DIGEST = re.compile(r'^sha256:[a-f0-9]{64}$')


def atomic(path, value):
    raw = json.dumps(value, sort_keys=True).encode()
    fd, temp = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def read(path, default):
    return json.loads(path.read_text()) if path.exists() else default


def digest(value):
    return 'sha256:' + hashlib.sha256(value).hexdigest()


def command(args, timeout=90):
    result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, timeout=timeout, check=True,
                            env={'PATH': '/usr/local/bin:/usr/bin:/bin', 'HOME': '/root'},
                            start_new_session=True)
    if len(result.stdout) > 1024 * 1024:
        raise ValueError('command output exceeded budget')
    return result.stdout.decode().strip()


def compose_args(config, env_file):
    return ['docker', 'compose', '--project-name', config['project'], '--file',
            config['compose_file'], '--env-file', str(env_file)]


def snapshot(config):
    root = Path(config['root'])
    if config['kind'] == 'systemd':
        service_user = command(['systemctl', 'show', '--property=User', '--value', config['unit']], 10)
        if not service_user or service_user in ('root', '0', os.environ.get('SUDO_USER', '')) or pwd.getpwnam(service_user).pw_uid == 0:
            raise ValueError('service must use a separate unprivileged account')
        current = root / 'current' / config['binary_name']
        identity = digest(current.read_bytes()) if current.exists() else 'absent'
        try:
            active = command(['systemctl', 'is-active', config['unit']], 10) == 'active'
        except subprocess.CalledProcessError:
            active = False
    else:
        # Inspect by immutable project/service labels; do not resolve mutable env files.
        ids = command(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=' + config['project'],
                       '--filter', 'label=com.docker.compose.service=' + config['service']], 10).split()
        if len(ids) > 1:
            raise ValueError('multiple containers require operator reconciliation')
        if ids:
            container = json.loads(command(['docker', 'inspect', ids[0]], 10))[0]
            identity = container['Config']['Image']
            active = container['State']['Running']
        else:
            identity, active = 'absent', False
    configuration = command(['systemctl', 'cat', config['unit']], 10) if config['kind'] == 'systemd' else digest(Path(config['compose_file']).read_bytes())
    revision = digest(json.dumps({'target': config, 'identity': identity, 'configuration': configuration}, sort_keys=True).encode())
    return {'revision': revision, 'identity': identity, 'active': active}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError('artifact redirects are forbidden')


def package(config, request):
    url = request['artifact_url']
    prefix = config['artifact_url_prefix']
    if not prefix.endswith('/') or not url.startswith(prefix):
        raise ValueError('artifact origin is not registered')
    root = Path(config['root'])
    release = root / 'releases' / request['artifact_digest'][7:]
    if release.exists():
        # A prior release is only reusable if its recorded archive digest matches.
        if (release / '.artifact-digest').read_text().strip() != request['artifact_digest'] or digest((release / config['binary_name']).read_bytes()) != (release / '.binary-digest').read_text().strip():
            raise ValueError('release digest changed')
        return release
    root.joinpath('releases').mkdir(parents=True, exist_ok=True, mode=0o755)
    with tempfile.TemporaryDirectory(dir=root / 'releases') as temp:
        archive = Path(temp) / 'package.tar.gz'
        total = 0
        hasher = hashlib.sha256()
        with urllib.request.build_opener(NoRedirect()).open(url, timeout=30) as response, archive.open('wb') as out:
            while block := response.read(65536):
                total += len(block)
                if total > 128 * 1024 * 1024:
                    raise ValueError('artifact exceeds 128 MiB')
                hasher.update(block)
                out.write(block)
        if 'sha256:' + hasher.hexdigest() != request['artifact_digest']:
            raise ValueError('artifact checksum mismatch')
        staging = Path(temp) / 'release'
        staging.mkdir(mode=0o755)
        # Parse exactly one ordinary tar header. General tar readers process PAX
        # extensions before returning a member and can allocate unbounded metadata.
        with gzip.open(archive, 'rb') as tar:
            header = tar.read(512)
            member = tarfile.TarInfo.frombuf(header, 'utf-8', 'strict')
            if not member.isreg() or member.name != config['binary_name'] or member.size < 0 or member.size > 128 * 1024 * 1024:
                raise ValueError('package must contain only the registered binary')
            binary = staging / config['binary_name']
            remaining = member.size
            with binary.open('wb') as out:
                while remaining:
                    data = tar.read(min(65536, remaining))
                    if not data:
                        raise ValueError('truncated binary')
                    out.write(data)
                    remaining -= len(data)
            padding = (-member.size) % 512
            if padding and tar.read(padding) != bytes(padding):
                raise ValueError('invalid tar padding')
            trailing = tar.read(10241)
            if len(trailing) > 10240 or len(trailing) < 1024 or any(trailing):
                raise ValueError('package contains extra entries or excessive padding')
            binary.chmod(0o755)
        (staging / '.artifact-digest').write_text(request['artifact_digest'])
        (staging / '.binary-digest').write_text(digest((staging / config['binary_name']).read_bytes()))
        os.rename(staging, release)
    return release


def matches(config, request, current):
    if not current['active']:
        return False
    if config['kind'] == 'compose':
        return current['identity'] == config['image_repository'] + '@' + request['artifact_digest']
    release = Path(config['root']) / 'releases' / request['artifact_digest'][7:]
    return release.exists() and (release / '.binary-digest').read_text().strip() == current['identity']


def process(request):
    if os.geteuid() != 0 or len(sys.argv) != 1:
        raise ValueError('root-owned stdin-only helper required')
    if CONFIG.stat().st_uid != 0 or CONFIG.stat().st_mode & 0o022:
        raise ValueError('unsafe target configuration permissions')
    target = request.get('target_id', '')
    if not IDENT.fullmatch(target):
        raise ValueError('invalid target')
    config = json.loads(CONFIG.read_text())['targets'][target]
    if config.get('kind') not in ('systemd', 'compose'):
        raise ValueError('unsupported target')
    root = Path(config['root'])
    if not root.is_absolute() or '..' in root.parts or not str(root).startswith('/opt/aiops-delivery/'):
        raise ValueError('unregistered release root')
    root.mkdir(parents=True, exist_ok=True)
    if root.is_symlink() or root.resolve() != root:
        raise ValueError('release root must not be a symlink')
    if root.stat().st_uid != 0 or root.stat().st_mode & 0o022:
        raise ValueError('release root must not be writable by the SSH account')
    if config['kind'] == 'systemd':
        if not re.fullmatch(r'[a-zA-Z0-9_.-]+\.service', config['unit']) or not IDENT.fullmatch(config['binary_name']):
            raise ValueError('invalid service or binary')
    else:
        if not IDENT.fullmatch(config['project']) or not IDENT.fullmatch(config['service']):
            raise ValueError('invalid compose registration')
        compose = Path(config['compose_file'])
        if compose.parent != root or compose.is_symlink() or compose.stat().st_uid != 0 or compose.stat().st_mode & 0o022:
            raise ValueError('unsafe compose template')
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory = STATE / target
    directory.mkdir(mode=0o700, exist_ok=True)
    state_file = directory / 'state.json'
    state = read(state_file, {})
    action = request.get('action')
    if action == 'inspect':
        return dict(snapshot(config), active_operation=state.get('active_operation', ''), status='ready')
    operation = request.get('operation_id', '')
    if not OP.fullmatch(operation):
        raise ValueError('invalid operation')
    record_file = directory / (operation + '.json')
    record = read(record_file, None)
    if action == 'status':
        if record is None:
            return {'status': 'not_found', 'operation_id': operation}
        if record['status'] in ('running', 'needs_attention', 'succeeded'):
            lock = (directory / 'target.lock').open('a')
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return dict(record, status='needs_attention') if record['status'] == 'succeeded' else record
            current = snapshot(config)
            if matches(config, record['request'], current):
                record.update(status='succeeded', observed=current)
                atomic(record_file, record)
                if state.get('active_operation') == operation:
                    atomic(state_file, {'active_operation': '', 'last_operation': operation})
            else:
                record['status'] = 'needs_attention'
                atomic(record_file, record)
            lock.close()
        return record
    if action != 'apply' or not DIGEST.fullmatch(request.get('artifact_digest', '')) or not DIGEST.fullmatch(request.get('expected_revision', '')) or not re.fullmatch(r'[a-f0-9]{64}', request.get('plan_digest', '')):
        raise ValueError('invalid apply intent')
    if record is not None:
        if {k: v for k, v in record['request'].items() if k != 'artifact_url'} != {k: v for k, v in request.items() if k != 'artifact_url'}:
            raise ValueError('operation payload changed')
        return record
    lock = (directory / 'target.lock').open('a')
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return {'status': 'busy', 'operation_id': operation}
    # Re-read under lock; state is never cleared merely because a process died.
    state = read(state_file, {})
    if state.get('active_operation'):
        return {'status': 'busy', 'operation_id': operation}
    if snapshot(config)['revision'] != request['expected_revision']:
        return {'status': 'drift', 'operation_id': operation}
    record = {'status': 'running', 'operation_id': operation, 'request': request}
    atomic(state_file, {'active_operation': operation})
    atomic(record_file, record)
    try:
        if config['kind'] == 'compose':
            image = config['image_repository'] + '@' + request['artifact_digest']
            # Registry pull validates OCI digest before replacement.
            command(['docker', 'pull', image], 180)
            env_file = directory / (operation + '.env')
            env_file.write_text('AIOPS_IMAGE=' + image + '\nAIOPS_OPERATION=' + operation + '\n')
            env_file.chmod(0o600)
            command(compose_args(config, env_file) + ['up', '-d', '--no-build', '--no-deps', config['service']], 90)
        else:
            release = package(config, request)
            link = root / ('current-' + operation)
            link.symlink_to(release)
            os.replace(link, root / 'current')
            command(['systemctl', 'restart', config['unit']], 90)
        current = snapshot(config)
        if not matches(config, request, current):
            raise ValueError('desired state did not converge')
        record.update(status='succeeded', observed=current)
        atomic(record_file, record)
        atomic(state_file, {'active_operation': '', 'last_operation': operation})
    except Exception:
        record['status'] = 'needs_attention'
        atomic(record_file, record)
    return record


if __name__ == '__main__':
    signal.signal(signal.SIGHUP, signal.SIG_IGN)
    try:
        raw = sys.stdin.buffer.read(65537)
        if len(raw) > 65536:
            raise ValueError('request exceeds budget')
        result = process(json.loads(raw))
    except Exception:
        result = {'status': 'rejected', 'message': 'Registered target, intent, or helper execution could not be verified'}
    try:
        print(json.dumps(result, sort_keys=True))
    except BrokenPipeError:
        pass
