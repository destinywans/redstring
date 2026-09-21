# Resources API — WAN-101 UI review contract

All URLs below are relative to `/api/v1`. Cookie session and application ACL apply. Data is read from PostgreSQL. Seeded records explicitly carry `is_mock: true`, `source_kind: "mock"`, `source_id: "ui-review-2026-09-21"`; mock health is never real infrastructure health.

## Types

```ts
type Option = { value: string; label: string; count: number };
type AppLink = { id: string; key: string; name: string; environment_id: string; environment_key: string; environment_name: string };
type Resource = {
 id:string; kind:string; name:string; description:string; external_id:string;
 source_kind:string; source_id:string; owner_user_id:string; owner_name:string;
 owner_team_id:string; team_name:string; applications:AppLink[];
 attributes:Record<string,unknown>; tags:string[]; is_mock:boolean;
 created_at:string; updated_at:string; observed_at?:string;
};
type Edge = { id:string; source_resource_id:string; target_resource_id:string;
 kind:string; provenance:'declared'|'discovered'; source_kind:string; source_id:string;
 description:string; is_mock:boolean; updated_at:string };
type Change = { id:number; action:string; description:string; created_at:string; is_mock:boolean };
```

Kinds: application, cluster, node, deployment, pod, event, namespace, host, network, storage, redis, mysql, kafka. All potentially absent metrics/config values must have an explicit unknown state in UI.

## List

`GET /resources?q=&kind=cluster&kind=node&applications=<app-id>&source_kind=mock&owner_id=<user-id>&team_id=<team-id>&environment_scope=production&page=1&page_size=10&sort=name`

Repeated filters are OR within a field, AND between fields. `application_id` and frontend `application` accepted as singular compatibility aliases. `environment_scope=all` or absent means all authorized environments; any other key is matched exactly, unknown key gives empty results. `page_size` 1–100; page 1-based. Sort values `name` (default), `updated_desc`, `kind`. The handler clamps pages after filters.

```ts
{ items:Resource[]; total:number; page:number; page_size:number;
 facets:{ kinds:Option[]; applications:Option[]; sources:Option[]; owners:Option[]; teams:Option[] } }
```

Facets cover the complete authorized/global-environment subset, before q/kind/etc filters. No explicit all option; omitted selection is unrestricted.

## Detail

`GET /resources/{id}?environment_scope=production`

```ts
{ resource:Resource; related_resources:Resource[]; relations:Edge[]; changes:Change[] }
```

AppLink (`applications[]`) is the only ownership source within the current authorized scope. Resource `attributes.application` and `attributes.application_id` are stripped on all reads, including legacy records, to prevent shared resources exposing an unauthorized application through redundant metadata. The seed does not write these attributes. Both endpoints of each returned relationship must be visible. Application links are trimmed to authorized applications and requested environment. A resource outside requested scope returns 404.

## Graph

`GET /resource-relations?root_id=<resource-id>&depth=2&kind=depends_on&resource_kind=redis&q=&application_id=<app-id>&environment_scope=production`

`hops` is accepted as an alias of depth, 1–3; default 2. No root returns the filtered graph. q/root/application/resource_kind select initial nodes, depth traverses visible matching edges in both directions. Relationship kind filters edges, not resource kinds. Returns `{ nodes:Resource[]; edges:Edge[]; truncated:boolean }`; capped at 300 nodes/600 edges. Layout belongs to frontend. Resource `kind=application` provides application graph nodes.

## Manual create

Admin-only `POST /resources` accepts `{name,kind,description?,external_id?,applications:[{id,environment_id}],owner_user_id?,owner_team_id?,attributes?,tags?}`. Each application/environment pair is checked. Record source is `manual`, `is_mock=false` (registration does not mean observed health), timestamps come from server. Returns Resource, status 201. Source/provenance flags cannot be chosen by browser. This registers metadata only, never provisions infrastructure.

## Infrastructure attributes (seeded examples)

Shared: `status`, `cluster`, `namespace`, `ip`, `version`, `cpu_percent`, `memory_percent`, `cpu_cores`, `memory_gb`, `age`, `metrics` (named numbers), `history` ({time,cpu,memory}[]), `labels` (object), `configuration` (object), `logs` (string[]).

Specific: cluster `nodes_count`,`pods_count`,`provider`; node `roles`,`pod_count`,`os`; deployment `replicas`,`ready_replicas`,`image`,`strategy`; pod `ready`,`restarts`,`node`,`image`; event `reason`,`message`,`severity`,`involved_object`,`count`,`first_seen`,`last_seen`; namespace `workloads_count`,`pods_count`,`quota`; host `os`,`region`,`disk_percent`; network `resource_type`,`address`,`ports`,`protocol`; storage `resource_type`,`capacity_gb`,`used_gb`,`storage_class`,`access_mode`; middleware `connections`,`port`,`role`,`database_size_mb`.

## Wiring and seed

The resources module owns Register(api); parent calls `resources.New(store.DB).Register(api)` after constructing authenticated server. Migration `011_resources.sql` is additive. Explicit seed script uses the existing local runtime DB configuration only on request; it creates isolated mock-source rows and never updates existing applications, bindings, users or teams. No automatic startup seed.

## Validation and limits

`TEST_DATABASE_URL=... GOCACHE=/tmp/aiops-resource-go-cache go test ./internal/resources -count=1` runs only this module. The PG test creates a random `resources_test_*` schema, applies migrations there, inserts fixture-only users/applications/sessions, verifies shared-resource application-link clipping, both-endpoint graph ACL, unknown scope, inaccessible detail/root 404, viewer create rejection, environment/application mismatch, manual-source flags and creation audit, then drops only its schema.

This UI-review implementation loads at most 10,000 authorized resources / 20,000 relationships before applying bounded filtering/traversal. Exceeding these limits fails explicitly instead of claiming a complete result. Move list predicates/count/facets fully into SQL and use indexed recursive graph queries before scaling beyond this UI-review dataset. Diagram positions are not persisted by this API.

Seed command (explicit local only):

```sh
python3 scripts/seed-resource-demo.py --runtime .local/product-lab/runtime.json --container aiops-canvas-db-1 --dry-run
python3 scripts/seed-resource-demo.py --runtime .local/product-lab/runtime.json --container aiops-canvas-db-1
```

For the existing seven application records this generates 50 resources and 28 relations. App mapping uses existing keys (checkout/order/message/VM) and actual environment IDs/keys; shared cluster/node/namespace links stay within corresponding scope. The seed transaction refuses ID collisions with a non-seed source, only rebuilds its own resource associations, and preserves existing application profiles and connector bindings. The dry run makes no writes.
