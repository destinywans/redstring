#!/usr/bin/env python3
"""Wire the separate aiops-test delivery lab into the isolated review workspace.

No credentials are printed or committed. Existing application bindings are not
rewritten. This registers product configuration; it never triggers a pipeline.
"""
import argparse
import importlib.util
import json
from pathlib import Path
import secrets
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parent.parent
LOCAL = ROOT / '.local/product-lab'


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    path.chmod(0o600)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lab', type=Path, required=True)
    args = parser.parse_args()
    lab = args.lab.resolve() / '.local/delivery'
    runtime = json.loads((LOCAL / 'runtime.json').read_text())
    db = urlsplit(runtime['DATABASE_URL'])
    if runtime.get('AIOPS_PUBLIC_ORIGIN') != 'http://127.0.0.1:18081' or db.hostname not in ('localhost', '127.0.0.1') or '_review_' not in db.path:
        raise RuntimeError('Only the isolated loopback review workspace is supported')
    spec = importlib.util.spec_from_file_location('local_lab', ROOT / 'scripts/connect-local-lab.py')
    helper = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(helper)
    api = helper.Client()
    connections = json.loads((lab / 'connections.json').read_text())
    gitlab = json.loads((lab / 'gitlab-demo.json').read_text())
    app = next((a for a in api.call('GET', '/applications')['items'] if a.get('key') == 'delivery-demo'), None)
    if not app:
        me = api.call('GET', '/auth/me')['user']
        app = api.call('POST', '/applications', {'key': 'delivery-demo', 'name': 'Delivery Demo', 'description': '本机实验模拟生产；专用交付集群与发布 VM，演示构建、审批和健康验证。', 'environment': 'delivery-lab', 'maintainer_id': me['id'], 'repository_url': gitlab['url'], 'tags': ['delivery-lab', 'synthetic-business-data'], 'profile': {'language': 'Go', 'service_type': 'API', 'criticality': '非生产', 'lifecycle': '实验', 'default_branch': gitlab['branch']}})
    envs = api.call('GET', f"/applications/{app['id']}/environments")['items']
    env = next(e for e in envs if e['key'] == 'delivery-lab')
    if env['name'] != '生产环境':
        env = api.call('PATCH', '/environments/' + env['id'], {
            'key': env['key'], 'name': '生产环境', 'archived': env['archived'],
            'binding_revision': env['binding_revision'], 'configuration_revision': env['configuration_revision'],
            'bindings': env.get('bindings') or [], 'configuration': env.get('configuration') or {}})
    webhook = LOCAL / 'delivery-webhook-token'
    if not webhook.exists():
        webhook.write_text(secrets.token_urlsafe(40))
        webhook.chmod(0o600)
    # Private credential values emitted by the lab are materialized as files for
    # server configuration, not sent through ordinary task input or browser APIs.
    credential_files = {}
    for key in ('s3_access_key', 's3_secret_key'):
        target = LOCAL / ('delivery-' + key)
        target.write_text(connections[key])
        target.chmod(0o600)
        credential_files[key] = str(target)
    conf = {
      'gitlabs': [{'id': 'local-gitlab', 'name': 'GitLab · 本机交付实验', 'base_url': connections['gitlab_url'], 'allow_http': True, 'token_file': str(lab / 'gitlab-delivery-token'), 'webhook_token_file': str(webhook), 'bindings': [{'application_id': app['id'], 'environment_id': env['id'], 'project_id': gitlab['project_id'], 'allowed_refs': [gitlab['branch']], 'allow_actions': True, 'external_governance': True, 'manual_jobs': ['manual-check']}]}],
      'argo': {'base_url': connections['workflows_url'], 'token_file': connections['workflows_token_file'], 'allow_http': True, 'namespace': connections['workflows_namespace'], 'service_account': 'aiops-build', 'parallelism': 2, 'artifact_repository_name': 'artifact-repositories', 'artifact_repository_key': 'default-v1'},
      's3': {'endpoint': connections['s3_endpoint'], 'read_endpoint': 'http://host.lima.internal:28333', 'bucket': connections['s3_bucket'], 'region': 'us-east-1', 'allow_http': True, 'access_key_file': credential_files['s3_access_key'], 'secret_key_file': credential_files['s3_secret_key']},
      'artifact_url_prefixes': [connections['registry_cluster'], connections['s3_endpoint'] + '/' + connections['s3_bucket'], 's3://' + connections['s3_bucket']]
    }
    save(LOCAL / 'delivery-config.json', conf)
    deployment_source = LOCAL / 'deployment-config.json'
    if deployment_source.exists():
        deployments = json.loads(deployment_source.read_text())
        for target in deployments['targets']:
            target['application_id'], target['environment_id'] = app['id'], env['id']
        deployments['data_dir'] = str(LOCAL / 'deployment-state')
        save(LOCAL / 'deployment-product-config.json', deployments)
        runtime['AIOPS_DEPLOYMENT_CONFIG'] = str(LOCAL / 'deployment-product-config.json')
    runtime['AIOPS_DELIVERY_CONFIG'] = str(LOCAL / 'delivery-config.json')
    save(LOCAL / 'runtime.json', runtime)
    save(LOCAL / 'delivery-binding.json', {'application_id': app['id'], 'environment_id': env['id']})
    print('Registered Delivery Demo and private delivery configuration. Restart the review API to apply. No run was triggered.')


if __name__ == '__main__':
    main()
