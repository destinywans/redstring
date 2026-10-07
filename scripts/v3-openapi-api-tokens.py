"""WAN-122 API token management and the one allowlisted Bearer read route."""

_api_token_scope = string(enum=[
    'services:view', 'events:view', 'connectors:view',
    'resources:view', 'logs:view', 'traces:view',
])
_api_token_scopes = {
    **arr(_api_token_scope), 'minItems': 1, 'maxItems': 6,
    'uniqueItems': True,
}
_api_token_issuable_scopes = {
    **arr(string(enum=['connectors:view'])), 'minItems': 1, 'maxItems': 1,
    'uniqueItems': True,
}
_api_token_ttl = integer(enum=[3600, 86400, 604800, 2592000])

S['V3APITokenCreate'] = obj({
    'name': string(minLength=1, maxLength=80,
                   description='Trimmed; 1-80 Unicode code points, at most 200 UTF-8 bytes; no control or format characters.'),
    'scopes': _api_token_issuable_scopes,
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
    'the only currently issuable scope is connectors:view; other planned read scopes '
    'lack a Bearer consumer and are rejected. The server generates a '
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
        ' These management routes require a human Cookie session. The only '
        'Bearer consumer is GET /connectors in one exact configured environment. '
        'API tokens cannot issue tokens, approve proposals, execute actions or '
        'authorize outbound Webhook redelivery.')

P['/connectors']['get']['security'] = [{'sessionCookie': []}, {'v3ApiToken': []}]
P['/connectors']['get']['description'] += (
    ' An alternative Bearer aiops_pat_ token is accepted only for this exact GET path, '
    'with no Cookie, one configured X-AIOps-Environment, connectors:view scope, '
    'a live human owner and current environment grant. Token verification and '
    'the environment-owned connector list run in one bounded database transaction. '
    'No other V3 read or write route accepts this token.')
v3_route('post','/api-tokens/{id}/rotate','Rotate an owned environment API token',role='human with scoped connectors.manage',
    response=obj({'id':string(),'secret_shown_once':string(),'old_expires_at':string(format='date-time')},['id','secret_shown_once','old_expires_at']),
    desc='Current human session, ownership, selected environment and current read/management grants required. The successor has the original token lifespan and scopes; only its SHA-256 digest is saved. The old credential expires at the earlier of its original expiry and 24 hours from rotation, never extending a short-lived token. Concurrent/repeated rotation of the same token returns 409. Active-token cap applies to overlap. Success, old expiry and audit share one transaction; no-store one-time secret response.')
