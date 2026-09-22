# Resources API — WAN-101

All URLs below are relative to `/api/v1`. Cookie session, resource RBAC and application ACL apply. Data is read from PostgreSQL. The current local workspace uses synchronized real resources; the historical seeded examples described below were removed under WAN-111 and are not recreated at startup.

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

The knowledge graph locator passes `graph_eligible=true` to exclude missing resources, events and (by default) runtime instances before pagination, avoiding an apparently empty result page after client-side filtering. `include_runtime=true` permits Pods/containers in that locator. Other directory requests are unchanged.

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

### Knowledge graph exploration (2026-09-22)

`GET /resource-relations?mode=overview&environment_scope=all`

Returns virtual groups scoped by source/cluster/namespace, plus counts of real relationships crossing those groups. Grouping does not write resource objects or invent resource relationships. Non-Kubernetes objects use their independent source domain. Namespace labels may repeat across clusters; group IDs remain distinct.

`GET /resource-relations?mode=neighbors&group_id=<group-id>&offset=0&limit=30`

Returns a page of resources within a selected group and actual edges between the returned objects. `total` is the number of matching resources in that group.

`GET /resource-relations?mode=neighbors&root_id=<resource-id>&direction=both&offset=0&limit=30`

Returns the center plus one page of its direct neighbors. `direction=out|in|both` follows the stored source/target direction. `total`, `offset`, `remaining` count neighbors, excluding the center. The center is always retained. The UI merges explicit expansions and may collapse each expansion without refitting the entire scene.

For incremental requests, repeated `visible_id=<loaded-resource-id>` includes existing scene nodes when computing edges, so real links spanning separate pages are not lost. Each ID is rechecked against the current authorized and strictly filtered set. Context may belong to another group already reached by expanding a resource, while the actual group page remains restricted to that group. At most 300 distinct IDs and 300 nodes after merging are accepted; exceeding that budget is an explicit error, never silent page loss. Counts and next offsets continue to describe the requested group's members or center's neighbors, not the existing scene. `page_node_ids` identifies only the actual current page, excluding the center and context-only objects; the UI uses this membership to collapse branches while retaining nodes shared by other expansions.

```ts
type GraphGroup = {
 id:string; label:string; virtual:true; source_id:string; source_kind:string;
 cluster:string; namespace:string; count:number; resource_kinds:Option[];
};
type GraphGroupEdge = {
 source_group_id:string; target_group_id:string; kind:string; count:number; virtual:true;
};
type Exploration = {
 mode:'overview'|'neighbors'; root_id?:string;
 nodes:Resource[]; edges:Edge[]; page_node_ids:string[]; truncated:boolean;
 total:number; offset:number; limit:number; remaining:number; next_offset:number|null;
 groups:GraphGroup[]; group_edges:GraphGroupEdge[];
 facets:{resource_kinds:Option[]; relation_kinds:Option[]};
 limits:{resources:number; relationships:number; nodes:number; edges:number};
 warnings:string[];
};
```

Repeated `resource_kind`, relationship `kind`, `source_kind`, and application filters are OR within a field and AND between fields. Environment and authorization apply before grouping/counting. Type/source filters are strict; a center outside the requested scope is not exposed. Facet choices are computed before the resource/relationship type filters so a multi-select can add another type without first clearing its existing selection. `q` searches candidate resources, including the existing pinyin matching, rather than making unrelated neighbors match. Default exploration excludes runtime Pods/containers; `include_runtime=true` can include them, but events remain excluded. Search-and-locate in the UI uses the authorized resource directory and then selects an explicit center.

Responses expose remaining items and a nullable next offset. `limit` is bounded to 1–100. Hard graph caps remain 300 nodes / 600 edges; preloading remains bounded at 10,000 authorized resources / 20,000 relationships. These are reported limits, not claims of an unlimited graph database. The UI must report partial results or errors rather than showing an empty graph as complete.

### Legacy graph

`GET /resource-relations?root_id=<resource-id>&depth=2&kind=depends_on&resource_kind=redis&q=&application_id=<app-id>&environment_scope=production`

With no `mode`, `hops` is accepted as an alias of depth, 1–3; default 2. No root returns the filtered graph. q/root/application/resource_kind select initial nodes, depth traverses visible matching edges in both directions. Relationship kind filters edges, not resource kinds. Returns `{ nodes:Resource[]; edges:Edge[]; truncated:boolean }`; capped at 300 nodes/600 edges. Layout belongs to frontend. The knowledge graph page no longer requires synthetic application nodes as default roots.

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
