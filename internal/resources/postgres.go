package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
	"strings"
)

type Service struct{ DB *pgxpool.Pool }

func New(db *pgxpool.Pool) *Service { return &Service{DB: db} }

// visible reads only authorized application links. Shared resources must not expose
// names or environment IDs belonging to applications outside the user's grants.
func (s *Service) visible(ctx context.Context, apps []string, admin bool, scope string) ([]Resource, error) {
	if apps == nil {
		apps = []string{}
	}
	if scope == "all" {
		scope = ""
	}
	rows, err := s.DB.Query(ctx, `SELECT r.id,r.kind,r.name,r.description,r.external_id,r.source_kind,r.source_id,
 COALESCE(r.owner_user_id,''),COALESCE(u.name,''),COALESCE(r.owner_team_id,''),COALESCE(t.name,''),
 r.attributes,r.tags,r.is_mock,r.created_at,r.updated_at,r.observed_at,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'key',a.key,'name',a.name,
 'environment_id',e.id,'environment_key',e.key,'environment_name',e.name) ORDER BY a.key,e.key)
 FROM cmdb_resource_applications l JOIN applications a ON a.id=l.application_id
 JOIN application_environments e ON e.id=l.environment_id AND e.application_id=a.id
 WHERE l.resource_id=r.id AND a.id=ANY($1::text[]) AND ($3='' OR e.key=$3)), '[]')
 FROM cmdb_resources r LEFT JOIN users u ON u.id=r.owner_user_id LEFT JOIN teams t ON t.id=r.owner_team_id
 WHERE EXISTS(SELECT 1 FROM cmdb_resource_applications l JOIN application_environments e ON e.id=l.environment_id
 WHERE l.resource_id=r.id AND l.application_id=ANY($1::text[]) AND ($3='' OR e.key=$3))
 OR ($2 AND $3='' AND NOT EXISTS(SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id))
 OR ($3='' AND EXISTS(SELECT 1 FROM rbac_effective_permissions p WHERE p.user_id=$4 AND p.action='view' AND p.scope_type='resource' AND p.resource_id=r.id))
 ORDER BY r.name,r.id LIMIT 10001`, apps, admin, scope, store.Principal(ctx).ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Resource{}
	for rows.Next() {
		var r Resource
		var attrs, tags, links []byte
		if err = rows.Scan(&r.ID, &r.Kind, &r.Name, &r.Description, &r.ExternalID, &r.SourceKind, &r.SourceID, &r.OwnerUserID, &r.OwnerName, &r.OwnerTeamID, &r.TeamName, &attrs, &tags, &r.IsMock, &r.CreatedAt, &r.UpdatedAt, &r.ObservedAt, &links); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(attrs, &r.Attributes); err != nil {
			return nil, err
		}
		// AppLink is the sole scope-aware ownership source. Legacy attributes must
		// not bypass its authorization clipping on shared resources.
		delete(r.Attributes, "application")
		delete(r.Attributes, "application_id")
		if err = json.Unmarshal(tags, &r.Tags); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(links, &r.Applications); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if len(out) > 10000 {
		return nil, fmt.Errorf("resource review dataset exceeds bounded query limit")
	}
	return out, rows.Err()
}
func (s *Service) edges(ctx context.Context, visible []Resource) ([]Relation, error) {
	ids := make([]string, 0, len(visible))
	for _, r := range visible {
		ids = append(ids, r.ID)
	}
	rows, err := s.DB.Query(ctx, `SELECT id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id,description,is_mock,updated_at FROM cmdb_resource_relations WHERE source_resource_id=ANY($1::text[]) AND target_resource_id=ANY($1::text[]) ORDER BY id LIMIT 20001`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Relation{}
	for rows.Next() {
		var e Relation
		if err = rows.Scan(&e.ID, &e.SourceResourceID, &e.TargetResourceID, &e.Kind, &e.Provenance, &e.SourceKind, &e.SourceID, &e.Description, &e.IsMock, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if len(out) > 20000 {
		return nil, errors.New("resource review relationship dataset exceeds bounded query limit")
	}
	return out, rows.Err()
}
func (s *Service) changes(ctx context.Context, id string) ([]Change, error) {
	rows, e := s.DB.Query(ctx, `SELECT id,action,description,is_mock,created_at FROM cmdb_resource_changes WHERE resource_id=$1 ORDER BY id DESC LIMIT 100`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var v Change
		if e = rows.Scan(&v.ID, &v.Action, &v.Description, &v.IsMock, &v.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type CreateInput struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Description  string `json:"description"`
	ExternalID   string `json:"external_id"`
	OwnerUserID  string `json:"owner_user_id"`
	OwnerTeamID  string `json:"owner_team_id"`
	Applications []struct {
		ID            string `json:"id"`
		EnvironmentID string `json:"environment_id"`
	} `json:"applications"`
	Attributes map[string]any `json:"attributes"`
	Tags       []string       `json:"tags"`
}

var kinds = map[string]string{"application": "应用", "cluster": "集群", "node": "节点", "deployment": "工作负载", "pod": "Pod", "event": "Kubernetes 事件", "namespace": "命名空间", "host": "主机", "network": "网络", "storage": "存储", "redis": "Redis", "mysql": "MySQL", "kafka": "Kafka"}

func (s *Service) create(ctx context.Context, in CreateInput) (string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 || kinds[in.Kind] == "" || len(in.Description) > 4000 || len(in.ExternalID) > 500 || len(in.Applications) > 50 || len(in.Tags) > 50 {
		return "", store.ErrInvalidReference
	}
	// Metadata registration cannot report a collected health state.
	if in.Attributes == nil {
		in.Attributes = map[string]any{}
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	attrs, e := json.Marshal(in.Attributes)
	if e != nil || len(attrs) > 65536 {
		return "", store.ErrInvalidReference
	}
	tags, _ := json.Marshal(in.Tags)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	for _, a := range in.Applications {
		var exists bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM application_environments WHERE id=$1 AND application_id=$2 AND NOT archived)`, a.EnvironmentID, a.ID).Scan(&exists)
		if e != nil {
			return "", e
		}
		if !exists {
			return "", store.ErrInvalidReference
		}
	}
	id := store.ID()
	_, e = tx.Exec(ctx, `INSERT INTO cmdb_resources(id,name,kind,description,external_id,source_kind,source_id,owner_user_id,owner_team_id,attributes,tags) VALUES($1,$2,$3,$4,$5,'manual','',NULLIF($6,''),NULLIF($7,''),$8,$9)`, id, in.Name, in.Kind, in.Description, in.ExternalID, in.OwnerUserID, in.OwnerTeamID, attrs, tags)
	if e != nil {
		return "", store.ClassifyError(e)
	}
	for _, a := range in.Applications {
		_, e = tx.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, a.ID, a.EnvironmentID)
		if e != nil {
			return "", store.ClassifyError(e)
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO cmdb_resource_changes(resource_id,action,description) VALUES($1,'resource.created','手动登记资源资料；尚未采集运行状态')`, id)
	if e != nil {
		return "", e
	}
	changes, _ := json.Marshal(map[string]any{"resource_id": id, "name": in.Name, "kind": in.Kind, "source_kind": "manual"})
	_, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'resource.created',$2,'success',$3)`, store.Principal(ctx).ID, id, changes)
	if e != nil {
		return "", e
	}
	return id, tx.Commit(ctx)
}
