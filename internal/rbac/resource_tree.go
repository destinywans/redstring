package rbac

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/wans/aiops-canvas/internal/store"
)

type ResourceTreeItem struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ScopeType        string   `json:"scope_type"`
	Kind             string   `json:"kind"`
	Description      string   `json:"description"`
	IsMock           bool     `json:"is_mock"`
	Missing          bool     `json:"missing"`
	AssignedActions  []string `json:"assigned_actions"`
	AvailableActions []string `json:"available_actions"`
}
type ResourceTreeGroup struct {
	Kind    string             `json:"kind"`
	Label   string             `json:"label"`
	Total   int                `json:"total"`
	Offset  int                `json:"offset"`
	HasMore bool               `json:"has_more"`
	Items   []ResourceTreeItem `json:"items"`
}

func resourceGroupLabel(kind string) string {
	labels := map[string]string{"application": "应用", "legacy_application": "历史应用授权", "management": "历史平台管理对象", "cluster": "集群", "node": "节点", "workload": "工作负载", "pod": "Pod 与容器", "namespace": "命名空间", "host": "主机", "vm": "虚拟机", "network": "网络", "storage": "存储", "database": "数据库", "middleware": "中间件", "unavailable": "已失效资源", "deployment": "Deployment", "event": "Kubernetes 事件", "redis": "Redis", "mysql": "MySQL", "kafka": "Kafka"}
	key := strings.TrimPrefix(kind, "resource:")
	if label := labels[key]; label != "" {
		return label
	}
	return key
}

func (s *Service) resourceTree(w http.ResponseWriter, r *http.Request) {
	permission, err := s.Permission(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	limit, offset := 50, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if value := r.URL.Query().Get(key); value != "" {
			parsed, e := strconv.Atoi(value)
			if e != nil {
				respond(w, nil, store.ErrInvalidReference)
				return
			}
			*target = parsed
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		respond(w, nil, store.ErrInvalidReference)
		return
	}
	cte := `WITH catalog AS (
 SELECT r.id,r.name,'resource'::text scope_type,r.kind,r.description,r.is_mock,false missing,'resource:'||r.kind group_key FROM cmdb_resources r WHERE rbac_is_infrastructure_kind(r.kind)
 ), filtered AS (SELECT * FROM catalog WHERE aiops_search_match(concat_ws(' ',name,id,description),$2) AND ($3='' OR group_key=$3) AND $1::text IS NOT NULL)`
	args := []any{permission.ID, strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("kind")}
	rows, err := s.Store.DB.Query(r.Context(), cte+` SELECT group_key,count(*) FROM filtered GROUP BY group_key ORDER BY group_key`, args...)
	if err != nil {
		respond(w, nil, err)
		return
	}
	groups := []ResourceTreeGroup{}
	indices := map[string]int{}
	for rows.Next() {
		var group ResourceTreeGroup
		if err = rows.Scan(&group.Kind, &group.Total); err != nil {
			rows.Close()
			respond(w, nil, err)
			return
		}
		group.Label = resourceGroupLabel(group.Kind)
		group.Offset = offset
		group.HasMore = offset+limit < group.Total
		group.Items = []ResourceTreeItem{}
		indices[group.Kind] = len(groups)
		groups = append(groups, group)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		respond(w, nil, err)
		return
	}
	args = append(args, offset, limit)
	rows, err = s.Store.DB.Query(r.Context(), cte+`, ranked AS (SELECT *,row_number() OVER(PARTITION BY group_key ORDER BY name,id) rn FROM filtered) SELECT id,name,scope_type,kind,description,is_mock,missing,group_key,COALESCE((SELECT array_agg(action ORDER BY action) FROM rbac_permission_resources x WHERE x.permission_id=$1 AND x.scope_type=ranked.scope_type AND x.resource_id=ranked.id),'{}'::text[]) FROM ranked WHERE rn>$4 AND rn<=$4+$5 ORDER BY group_key,rn`, args...)
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item ResourceTreeItem
		var groupKey string
		if err = rows.Scan(&item.ID, &item.Name, &item.ScopeType, &item.Kind, &item.Description, &item.IsMock, &item.Missing, &groupKey, &item.AssignedActions); err != nil {
			respond(w, nil, err)
			return
		}
		item.AvailableActions = availableActions(item.ScopeType)
		if item.Missing {
			item.AvailableActions = []string{}
		}
		if index, ok := indices[groupKey]; ok {
			groups[index].Items = append(groups[index].Items, item)
		}
	}
	respond(w, map[string]any{"revision": permission.Revision, "groups": groups}, rows.Err())
}
