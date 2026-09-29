"""WAN-122 API token domain fragment.

This file is intentionally not loaded by v3-openapi.py yet. WAN-117 must
connect the domain router, bearer verifier and aggregate contract together.
"""

_api_token_scope = string(enum=[
    'services:view', 'events:view', 'connectors:view',
    'resources:view', 'logs:view', 'traces:view',
])
_api_token_scopes = {
    **arr(_api_token_scope), 'minItems': 1, 'maxItems': 6,
    'uniqueItems': True,
}
_api_token_ttl = integer(enum=[3600, 86400, 604800, 2592000])

S['V3APITokenCreate'] = obj({
    'name': string(minLength=1, maxLength=80,
                   description='Trimmed; 1-80 Unicode code points, at most 200 UTF-8 bytes; no control or format characters.'),
    'scopes': _api_token_scopes,
    'ttl': _api_token_ttl,
}, ['name', 'scopes', 'ttl'])
S['V3APITokenMetadata'] = obj({
    'id': string(),
    'name': string(),
    'environment_id': string(),
    'scopes': _api_token_scopes,
    'created_at': string(format='date-time'),
    'expires_at': string(format='date-time'),
    'revoked_at': {'anyOf': [string(format='date-time'), {'type': 'null'}]},
}, ['id', 'name', 'environment_id', 'scopes', 'created_at',
    'expires_at', 'revoked_at'])
S['V3APITokenCreated'] = obj({
    **{key: value for key, value in S['V3APITokenMetadata']['properties'].items()
       if key != 'revoked_at'},
    'secret_shown_once': string(minLength=1),
}, ['id', 'name', 'environment_id', 'scopes', 'created_at',
    'expires_at', 'secret_shown_once'])
S['V3APITokenList'] = obj({
    'items': arr(ref('V3APITokenMetadata')),
    'has_more': v3_bool(),
}, ['items', 'has_more'])

_api_token_human_boundary = (
    'Requires one selected environment, an active human Cookie session, '
    'current environment-scoped connectors.manage authority and a current '
    'environment-level grant for every requested token scope. Machine tokens cannot call these '
    'routes. Account, session, environment and grants are checked at the '
    'transaction boundary; no token can exceed its issuing human\'s current '
    'rights.')
v3_route('post', '/api-tokens', 'Create an environment-scoped API token',
    role='human with scoped connectors.manage',
    body=ref('V3APITokenCreate'), response=ref('V3APITokenCreated'),
    status=201,
    desc=_api_token_human_boundary + ' Name is trimmed, 1-80 Unicode code '
    'points and at most 200 UTF-8 bytes, without control or format characters; '
    'ttl is one of 1 hour, 1 day, 7 days or 30 days in seconds; '
    'scopes are 1-6 distinct, exact read-only codes. The server generates a '
    '256-bit secret and returns it only in secret_shown_once on successful '
    'creation. Only its SHA-256 digest is persisted, with creation audit in '
    'the same transaction. The response and secret must not be cached or '
    'logged. The frozen frontend currently sends no request body and cannot '
    'display the returned secret.')
v3_route('get', '/api-tokens', 'List own API token metadata',
    role='human owner',
    response=ref('V3APITokenList'),
    desc='Requires one selected environment, an active human Cookie session '
    'and ownership. Listing remains available after grant removal so the '
    'owner can find credentials to revoke. Only the caller\'s token metadata '
    'in that environment is listed with usable tokens first, up to 100 '
    'entries, and has_more for older history; '
    'secret, digest and other users\' tokens are never returned. '
    'Listing does not append a management audit record.')
v3_route('delete', '/api-tokens/{id}', 'Revoke an own API token',
    role='human owner', status=204,
    desc='Requires one selected environment, an active human Cookie session '
    'and ownership, including after grant removal. An own token in the selected '
    'environment is revoked with audit in one transaction. Repeated DELETE '
    'of the same own token returns 204 without another audit entry. Missing and foreign token IDs return '
    '404 without revealing ownership.')

for _api_token_method, _api_token_path in (
        ('post', '/api-tokens'), ('get', '/api-tokens'),
        ('delete', '/api-tokens/{id}')):
    P[_api_token_path][_api_token_method]['description'] += (
        ' Bearer token authentication is not connected to the common HTTP '
        'gate by this domain package. WAN-117 must wire VerifyWithTx and '
        'enforce the token scope, current owner grant and object scope on '
        'each permitted read. API tokens cannot issue tokens, approve '
        'proposals, execute actions or authorize outbound Webhook redelivery.')
