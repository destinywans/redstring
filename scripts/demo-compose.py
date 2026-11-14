#!/usr/bin/env python3
"""Build and start the three Compose apps for an already provisioned local lab."""

import argparse
from contextlib import contextmanager
import fcntl
from hashlib import sha256
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import subprocess
import sys
import time
from urllib.parse import quote, urlsplit


ROOT = Path(__file__).resolve().parents[1]
LAB = ROOT.parent / "aiops-test"
ACCEPTANCE = ROOT / ".local" / "acceptance"
DELIVERY = ACCEPTANCE / "demo-complete" / "delivery"
OPERATIONS = ROOT / "ops" / "demo-compose"
STATE = ACCEPTANCE / "compose"
PROJECT = "redstring-demo-compose"
MINIMAL_PROJECT = "redstring-demo-minimal"

APP_PORTS = {"prod": 18090, "nonprod": 18091, "delivery": 18593}
PROXY_PORTS = {"prod": 18443, "nonprod": 18444}
DB_NAMES = {"prod": "redstring_prod", "nonprod": "redstring_nonprod", "delivery": "redstring_local_delivery_demo"}

# Existing local sources on the Docker Desktop host. No listener forwards
# arbitrary destinations, and the relays are inaccessible outside their netns.
RELAY_PORTS = (
    465, 2746, 4318, 13000, 13100, 13133, 13200, 13201, 18200, 18445, 18446,
    18491, 18492, 18493, 19090, 19093, 19200, 21434, 25432, 26443,
    27580, 27780, 27880, 28080, 28081, 28333,
)

COMPOSE_PROJECTS = {
    "aiops-test": ("mysql", "redis", "kafka", "prometheus", "rules-sync", "alertmanager", "loki", "grafana", "ollama"),
    "aiops-enterprise": ("elasticsearch", "tempo", "otel-collector", "vault"),
    "aiops-test-observability": ("mysql-exporter", "redis-exporter", "kafka-exporter"),
    "redstring-demo-observability": ("tempo",),
    "aiops-test-demo-complete": ("keycloak", "mcp", "receiver"),
    "aiops-delivery": ("buildkit", "seaweedfs", "registry", "gitlab", "runner"),
}


def command(*args, **options):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, **options)


def output(*args):
    return command(*args, stdout=subprocess.PIPE).stdout.strip()


def read_env(path):
    result = {}
    for line in path.read_text().splitlines():
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if separator != "=" or re.fullmatch(r"[A-Z][A-Z0-9_]*", key) is None or "\x00" in value:
            raise ValueError(f"invalid environment entry in {path.name}")
        result[key] = value
    return result


def private_write(path, text):
    temporary = path.with_suffix(path.suffix + ".tmp")
    with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "w") as handle:
        handle.write(text)
    temporary.replace(path)


def start_minimal():
    home = ROOT / ".local" / "demo-compose-minimal"
    home.mkdir(mode=0o700, parents=True, exist_ok=True)
    env_file = home / "compose.env"
    if not env_file.exists():
        private_write(env_file, "\n".join((
            "POSTGRES_PASSWORD=" + secrets.token_hex(24),
            "AIOPS_ADMIN_EMAIL=admin@localhost",
            "AIOPS_ADMIN_PASSWORD=" + secrets.token_urlsafe(24),
            "AIOPS_PORT=18780",
            "POSTGRES_PORT=18781",
            "OLLAMA_MODEL=",
            "EMBEDDING_PROVIDER=",
            "",
        )))
    buildx = home / "buildx"
    buildx.mkdir(mode=0o700, exist_ok=True)
    values = read_env(env_file)
    env = {k: v for k, v in os.environ.items() if not k.startswith(("POSTGRES_", "AIOPS_", "OLLAMA_", "EMBEDDING_"))}
    env["BUILDX_CONFIG"] = str(buildx)
    command("docker", "compose", "--env-file", str(env_file), "-p", MINIMAL_PROJECT,
            "-f", str(ROOT / "compose.yaml"), "--profile", "app", "up", "-d", "--build", "--wait", env=env)
    port = int(values["AIOPS_PORT"])
    if json.loads(local_health(port, path="/api/v1/instance")).get("tier") != "nonprod":
        raise ValueError("minimal demo returned an unexpected V3 instance")
    print(f"http://127.0.0.1:{port}/login")
    print(f"Credentials: {env_file} (AIOPS_ADMIN_EMAIL and AIOPS_ADMIN_PASSWORD)")


@contextmanager
def operation_lock():
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    with os.fdopen(os.open(STATE / "lock", os.O_WRONLY | os.O_CREAT, 0o600), "w") as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            raise ValueError("another demo Compose command is running") from exc
        yield


def compose(*args, runtime=False):
    env = os.environ.copy()
    buildx = STATE / "buildx"
    buildx.mkdir(mode=0o700, exist_ok=True)
    env.update(
        BUILDX_CONFIG=str(buildx),
        DEMO_UID=str(os.getuid()),
        DEMO_GID=str(os.getgid()),
        DEMO_REVISION=output("git", "rev-parse", "HEAD"),
        DEMO_TREE_STATE="working-tree" if output("git", "status", "--porcelain") else "clean",
    )
    invocation = ["docker", "compose", "--env-file", os.devnull, "-p", PROJECT, "-f", str(OPERATIONS / "compose.yaml")]
    if runtime:
        invocation.extend(("-f", str(STATE / "runtime.json")))
    command(*invocation, *args, env=env)


def source_fingerprint():
    digest = sha256()
    root_files = ("Dockerfile", ".dockerignore", "go.mod", "go.sum")
    source_folders = ("cmd", "demo", "internal", "migrations", "ops", "shared", "web")
    names = output("git", "ls-files", "-co", "--exclude-standard", "--", *root_files, *source_folders)
    for name in sorted(set(names.splitlines())):
        path = ROOT / name
        if path.is_file():
            digest.update(name.encode() + b"\0")
            digest.update(sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def prepare(only_ready=False):
    before = source_fingerprint()
    recorded = STATE / "build.json"
    identity = {
        "source": before,
        "revision": output("git", "rev-parse", "HEAD"),
        "uid": os.getuid(),
        "gid": os.getgid(),
    }
    if only_ready:
        if not recorded.exists() or json.loads(recorded.read_text()) != identity | {"images": demo_image_ids()}:
            raise ValueError("Compose images or checkout changed since prepare; run prepare first")
        return
    compose("build")
    if source_fingerprint() != before or output("git", "rev-parse", "HEAD") != identity["revision"]:
        raise ValueError("files changed during the Compose build; rerun prepare on the final checkout")
    private_write(recorded, json.dumps(identity | {"images": demo_image_ids()}, indent=2) + "\n")


def demo_image_ids():
    images = json.loads(output(
        "docker", "image", "inspect", "redstring-local-demo:runtime",
        "redstring-local-demo:delivery", "redstring-local-demo:relay",
    ))
    return [image["Id"] for image in images]


def inspect(names):
    rows = json.loads(output("docker", "inspect", *names))
    return {row["Name"].removeprefix("/"): row for row in rows}


def owned_dependencies():
    required = {
        f"{project}-{service}-1": {"com.docker.compose.project": project, "com.docker.compose.service": service}
        for project, services in COMPOSE_PROJECTS.items()
        for service in services
    }
    required.update({
        name: {"aiops.acceptance": "2026-10-09"}
        for name in ("aiops-acceptance-pg", "aiops-acceptance-proxy", "aiops-acceptance-smtpsink")
    })
    required["aiops-delivery-control-plane"] = {"io.x-k8s.kind.cluster": "aiops-delivery"}
    present = inspect(required.keys())
    for name, labels in required.items():
        row = present[name]
        actual = row["Config"].get("Labels") or {}
        if any(actual.get(key) != value for key, value in labels.items()):
            raise ValueError(f"container {name} has unexpected ownership labels")
    database = present["aiops-acceptance-pg"]
    if not any(mount["Name"] == "aiops-acceptance-pgdata" for mount in database["Mounts"] if mount["Type"] == "volume"):
        raise ValueError("acceptance database does not use the expected dedicated volume")
    return present


def local_connect(port):
    with socket.create_connection(("127.0.0.1", port), timeout=3):
        pass


def local_health(port, host=None, path="/healthz"):
    if host is None:
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=8)
    else:
        context = ssl.create_default_context(cafile=str(ACCEPTANCE / "tls" / "cert.pem"))
        connection = http.client.HTTPSConnection(host, port, timeout=8, context=context)
        connection._create_connection = lambda _address, *args: socket.create_connection(("127.0.0.1", port), *args)
    try:
        connection.request("GET", path, headers={"Host": f"{host or '127.0.0.1'}:{port}"})
        response = connection.getresponse()
        if response.status != 200:
            raise ValueError(f"{host or '127.0.0.1'}:{port}{path}: HTTP {response.status}")
        return response.read()
    finally:
        connection.close()


def start_dependencies(present):
    vault = present["aiops-enterprise-vault-1"]
    pending = [row["Id"] for row in present.values() if not row["State"]["Running"]]
    if pending:
        command("docker", "start", *pending, stdout=subprocess.DEVNULL)
    if not vault["State"]["Running"]:
        raise ValueError("Vault development container restarted. Restore its in-memory secrets from the existing lab backup, then rerun")


def deployment_config():
    path = DELIVERY / "legacy-deployment.json"
    data = json.loads(path.read_text())
    targets = data["targets"]
    if {target["id"] for target in targets} != {"k8s-gitops", "vm-compose", "vm-systemd"}:
        raise ValueError("unexpected delivery target IDs")
    expected_key = LAB / ".local" / "delivery" / "deploy_ed25519"
    if data["data_dir"] != str(DELIVERY / "deployment-state"):
        raise ValueError("unexpected delivery working directory")
    vm_ports = set()
    for target in targets:
        vm = target.get("vm")
        if not vm:
            continue
        if vm["host"] != "127.0.0.1" or vm["private_key_file"] != str(expected_key):
            raise ValueError(f"{target['id']} is not an approved loopback deployment VM")
        port = vm["port"]
        if not isinstance(port, int) or not 1024 <= port <= 65535 or port in APP_PORTS.values():
            raise ValueError("invalid delivery VM port")
        vm_ports.add(port)
    return path, vm_ports


def bind(path, writable=False):
    if path.resolve() != path or not path.exists():
        raise ValueError(f"expected an existing non-symlink demo file or directory: {path}")
    return {"type": "bind", "source": str(path), "target": str(path), "read_only": not writable,
            "bind": {"create_host_path": False}}


def checked_database(url, name):
    parsed = urlsplit(url)
    if parsed.scheme not in ("postgres", "postgresql") or parsed.hostname != "127.0.0.1" or parsed.port != 25432:
        raise ValueError(f"{name} does not use the owned loopback PostgreSQL instance")
    if parsed.path != "/" + DB_NAMES[name]:
        raise ValueError(f"{name} has an unexpected database name")
    return url


def private_files_in(config, paths):
    for value in config:
        for match in re.findall(r"(?<![\w:./])/(?:Users|home|private|tmp)/[^\s,\"{}]+", value):
            path = Path(match)
            if not path.is_absolute() or not path.resolve().is_relative_to(ACCEPTANCE):
                raise ValueError(f"local config points outside the acceptance tree: {path.name}")
            paths.add(path)


def generate_runtime():
    config_path, vm_ports = deployment_config()
    runtime = {"services": {
        "relay": {
            "command": [str(port) for port in sorted(set(RELAY_PORTS) | vm_ports)],
            "ports": [f"127.0.0.1:{port}:{port}" for port in APP_PORTS.values()],
        }
    }}
    for tier in ("prod", "nonprod"):
        directory = ACCEPTANCE / tier
        env = read_env(directory / "server.env") | read_env(directory / "extra.env")
        origin = f"https://{tier}.redstring.test:{PROXY_PORTS[tier]}"
        if env.get("AIOPS_PUBLIC_ORIGIN") != origin:
            raise ValueError(f"unexpected public origin for {tier}")
        if env.get("AIOPS_V3_INSTANCE_CONFIG") != str(directory / "instance.json"):
            raise ValueError(f"unexpected V3 scope for {tier}")
        checked_database(env["DATABASE_URL"], tier)
        env.update(AIOPS_ADDR=f"0.0.0.0:{APP_PORTS[tier]}", AIOPS_WEB_DIR="/app/web", AIOPS_TRUSTED_PROXY_CIDRS="")
        paths = set()
        private_files_in(env.values(), paths)
        for path in tuple(paths):
            if path.suffix == ".json":
                private_files_in([path.read_text()], paths)
        runtime["services"][tier] = add_runtime(tier, env, sorted(paths))
    password = quote(read_env(ACCEPTANCE / "pg.env")["POSTGRES_PASSWORD"], safe="")
    secrets = json.loads((DELIVERY / "legacy-secrets.json").read_text())
    delivery_url = checked_database(f"postgres://aiops:{password}@127.0.0.1:25432/{DB_NAMES['delivery']}?sslmode=disable", "delivery")
    delivery_config = DELIVERY / "legacy-delivery.json"
    delivery_env = {
        "DATABASE_URL": delivery_url,
        "AIOPS_ADDR": f"0.0.0.0:{APP_PORTS['delivery']}",
        "AIOPS_PUBLIC_ORIGIN": f"http://127.0.0.1:{APP_PORTS['delivery']}",
        "AIOPS_ADMIN_EMAIL": secrets["email"],
        "AIOPS_ADMIN_PASSWORD": secrets["password"],
        "AIOPS_DELIVERY_CONFIG": str(delivery_config),
        "AIOPS_DEPLOYMENT_CONFIG": str(config_path),
    }
    paths = {delivery_config, config_path}
    private_files_in(
        [p.read_text().replace(str(LAB / ".local" / "delivery" / "deploy_ed25519"), "") for p in tuple(paths)],
        paths,
    )
    paths.add(LAB / ".local" / "delivery" / "deploy_ed25519")
    runtime["services"]["delivery"] = add_runtime("delivery", delivery_env, sorted(paths))
    private_write(STATE / "runtime.json", json.dumps(runtime, indent=2) + "\n")
    return vm_ports


def add_runtime(name, env, paths):
    env_file = STATE / f"{name}.env"
    private_write(env_file, "".join(f"{key}={value}\n" for key, value in env.items()))
    # Atomic token-file replacement changes the mounted inode. Use metadata only
    # so Compose renews the affected mount on up.
    version = sha256()
    for path in paths:
        if path.is_file():
            stat = path.stat()
            version.update(f"{path}\0{stat.st_ino}\0{stat.st_mtime_ns}\n".encode())
    return {"env_file": [{"path": str(env_file), "format": "raw"}],
            "environment": {"REDSTRING_DEMO_FILE_VERSION": version.hexdigest()},
            "volumes": [bind(path, writable=path == DELIVERY / "deployment-state") for path in paths]}


def check_ports():
    relay = subprocess.run(["docker", "inspect", PROJECT + "-relay-1"], text=True, capture_output=True)
    if relay.returncode == 0:
        row = json.loads(relay.stdout)[0]
        if row["Config"].get("Labels", {}).get("com.docker.compose.project") != PROJECT:
            raise ValueError("refusing an unrelated container with the demo relay name")
        if row["State"]["Running"]:
            return
    for port in APP_PORTS.values():
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
            # A recently stopped host process may leave accepted connections in
            # TIME_WAIT. SO_REUSEADDR still rejects an active TCP listener.
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                listener.bind(("127.0.0.1", port))
                listener.listen(1)
            except OSError as exc:
                raise ValueError(f"127.0.0.1:{port} is in use; stop the matching owned host demo process before cutover") from exc


def health():
    expected_urls = [f"https://{tier}.redstring.test:{port}/login" for tier, port in PROXY_PORTS.items()]
    expires = time.monotonic() + 20
    while True:
        try:
            local_health(APP_PORTS["delivery"])
            for tier, port in PROXY_PORTS.items():
                host = f"{tier}.redstring.test"
                local_health(port, host)
                info = json.loads(local_health(port, host, "/api/v1/instance"))
                expected = "production" if tier == "prod" else "nonprod"
                if info.get("tier") != expected or info.get("domain") != host:
                    raise ValueError(f"{tier} returned an unexpected V3 instance")
            break
        except (OSError, ValueError):
            if time.monotonic() >= expires:
                raise
            time.sleep(1)
    print(*expected_urls, "http://127.0.0.1:18593/legacy/delivery/pipelines", sep="\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "up", "status", "stop"))
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--acceptance", action="store_true", help="operate on the owned local acceptance lab")
    mode.add_argument("--minimal", action="store_true", help="bootstrap a dedicated empty V3 database and application")
    parser.add_argument("--prepared", action="store_true", help="reuse images prepared from this exact checkout")
    args = parser.parse_args()
    if os.getuid() == 0:
        raise ValueError("run as the owner of the local acceptance files instead of root")
    if args.minimal:
        if args.action != "up" or args.prepared:
            parser.error("--minimal only supports up")
        start_minimal()
        return
    with operation_lock():
        if args.action == "stop":
            compose("stop", runtime=True)
            return
        if args.action == "status":
            compose("ps", runtime=True)
            health()
            return
        owned = owned_dependencies()
        vm_ports = generate_runtime()
        compose("config", "--quiet", runtime=True)
        if args.action == "prepare":
            prepare()
            return
        # Build before resuming dependencies. A failed build never touches the running lab.
        prepare(args.prepared)
        check_ports()
        start_dependencies(owned)
        for port in (25432, 26443, 27580, 27780, 27880, *sorted(vm_ports)):
            local_connect(port)
        try:
            compose("up", "-d", "--wait", "--wait-timeout", "150", "--no-build", runtime=True)
            health()
        except (OSError, ValueError, subprocess.CalledProcessError):
            compose("stop", runtime=True)
            raise


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.CalledProcessError) as error:
        print(f"demo Compose: {error}", file=sys.stderr)
        sys.exit(1)
