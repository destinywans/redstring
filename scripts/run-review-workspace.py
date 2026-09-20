#!/usr/bin/env python3
"""Start/stop the explicitly configured local review API with private environment."""
import argparse
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import time
import urllib.request
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parent.parent
LOCAL = ROOT / '.local/product-lab'
BINARY = ROOT / '.local/aiops-product-check'


def stop():
    path = LOCAL / 'api.pid'
    if not path.exists():
        return
    pid = int(path.read_text())
    proc = subprocess.run(['ps', '-p', str(pid), '-o', 'comm='], capture_output=True, text=True)
    if proc.returncode == 0:
        if proc.stdout.strip() != str(BINARY):
            raise RuntimeError('PID no longer belongs to this review binary; refusing to signal it')
        os.kill(pid, signal.SIGTERM)
        for _ in range(100):
            if subprocess.run(['ps', '-p', str(pid)], capture_output=True).returncode != 0:
                break
            time.sleep(.2)
        else:
            raise RuntimeError('Review process is still shutting down')
    path.unlink(missing_ok=True)


def start():
    config = json.loads((LOCAL / 'runtime.json').read_text())
    database = urlsplit(config.get('DATABASE_URL', ''))
    if (config.get('AIOPS_ADDR') != '127.0.0.1:18081'
            or config.get('AIOPS_PUBLIC_ORIGIN') != 'http://127.0.0.1:18081'
            or database.hostname not in ('localhost', '127.0.0.1')
            or not database.path.startswith('/aiops_') or '_review_' not in database.path):
        raise RuntimeError('Refusing to launch outside the isolated loopback review workspace')
    if not BINARY.is_file():
        raise RuntimeError('Build .local/aiops-product-check before starting')
    if (LOCAL / 'api.pid').exists():
        raise RuntimeError('A recorded process exists; stop it explicitly before restarting')
    host, port = config['AIOPS_ADDR'].rsplit(':', 1)
    with socket.socket() as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((host, int(port)))
    with (LOCAL / 'api.log').open('ab') as log:
        proc = subprocess.Popen([str(BINARY)], cwd=ROOT, env={**os.environ, **config}, stdin=subprocess.DEVNULL,
                                stdout=log, stderr=log, start_new_session=True)
    (LOCAL / 'api.pid').write_text(str(proc.pid))
    for _ in range(60):
        if proc.poll() is not None:
            raise RuntimeError('Review API exited; inspect private api.log')
        try:
            with urllib.request.urlopen(config['AIOPS_PUBLIC_ORIGIN'] + '/healthz', timeout=2) as response:
                if response.status == 200:
                    time.sleep(.2)
                    if proc.poll() is not None:
                        raise RuntimeError('New process exited; refusing to accept another server health check')
                    print('Review workspace ready at ' + config['AIOPS_PUBLIC_ORIGIN'])
                    return
        except OSError:
            pass
        time.sleep(.5)
    raise RuntimeError('Review API readiness timed out; process remains recorded')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['start', 'stop', 'restart'])
    command = parser.parse_args().command
    if command in ('stop', 'restart'):
        stop()
    if command in ('start', 'restart'):
        start()
