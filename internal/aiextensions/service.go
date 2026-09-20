package aiextensions

import (
	"context"
	"fmt"
	"github.com/wans/aiops-canvas/internal/store"
	"strings"
)

func (s *Service) List(ctx context.Context, app, kind string) ([]Record, error) {
	if err := s.authorize(ctx, app, "view"); err != nil {
		return nil, err
	}
	if kind != "skill" && kind != "prompt" && kind != "mcp" {
		return nil, ErrInvalid
	}
	if kind == "mcp" && store.Principal(ctx).Role != "admin" {
		return nil, store.ErrForbidden
	}
	return s.Repository.List(ctx, app, kind)
}

// ListWorkspace is explicit; legacy List(app, kind) remains app-only.
func (s *Service) ListWorkspace(ctx context.Context, kind string) ([]Record, error) {
	if kind != "skill" && kind != "prompt" {
		return nil, ErrInvalid
	}
	if err := s.authorizeRecord(ctx, Record{Kind: kind}, "view"); err != nil {
		return nil, err
	}
	return s.Repository.List(ctx, "", kind)
}
func (s *Service) Get(ctx context.Context, id string, version int) (Record, error) {
	r, e := s.Repository.Get(ctx, id, 0)
	if e != nil {
		return r, e
	}
	if e = s.authorizeRecord(ctx, r, "view"); e != nil {
		return Record{}, e
	}
	if r.Kind == "mcp" && store.Principal(ctx).Role != "admin" {
		return Record{}, store.ErrForbidden
	}
	if version > 0 {
		return s.Repository.Get(ctx, id, version)
	}
	return r, nil
}
func (s *Service) Save(ctx context.Context, r Record) (Record, error) {
	if e := validate(r); e != nil {
		return Record{}, e
	}
	if e := s.authorizeRecord(ctx, r, "operate"); e != nil {
		return Record{}, e
	}
	if store.Principal(ctx).Role == "viewer" || (r.Kind == "mcp" && store.Principal(ctx).Role != "admin") {
		return Record{}, store.ErrForbidden
	}
	if r.Kind == "mcp" && r.Enabled {
		existing, e := s.Repository.List(ctx, r.ApplicationID, "mcp")
		if e != nil {
			return Record{}, e
		}
		names := map[string]bool{}
		for _, other := range existing {
			if other.ID == r.ID || !other.Enabled || other.MCP == nil {
				continue
			}
			for _, binding := range other.MCP.Bindings {
				names[binding.Name] = true
			}
		}
		for _, binding := range r.MCP.Bindings {
			if names[binding.Name] {
				return Record{}, ErrInvalid
			}
			names[binding.Name] = true
		}
		if len(names) > 8 {
			return Record{}, ErrInvalid
		}
	}
	expected := r.Version
	if r.ID == "" {
		if expected != 0 {
			return Record{}, ErrInvalid
		}
		r.ID = store.ID()
	} else {
		old, e := s.Repository.Get(ctx, r.ID, 0)
		if e != nil {
			return Record{}, e
		}
		if old.ApplicationID != r.ApplicationID || old.Kind != r.Kind {
			return Record{}, store.ErrForbidden
		}
	}
	r.Version = expected + 1
	r.UpdatedBy = store.Principal(ctx).ID
	r.ContentHash = hash(r)
	return s.Repository.Save(ctx, r, expected)
}

// Compose returns complete immutable source snapshots for a user-confirmed message.
// The existing message endpoint persists this text with the conversation. Nothing
// is silently injected into model/system instructions or automatically executed.
func (s *Service) Compose(ctx context.Context, in Composition) (Composed, error) {
	if e := s.authorize(ctx, in.ApplicationID, "operate"); e != nil {
		return Composed{}, e
	}
	if store.Principal(ctx).Role == "viewer" || len(in.References) == 0 || len(in.References) > 8 || len(in.Question) > 8000 {
		return Composed{}, ErrInvalid
	}
	records, e := s.Repository.List(ctx, in.ApplicationID, "mcp")
	if e != nil {
		return Composed{}, e
	}
	available := map[string]bool{}
	for _, r := range records {
		if r.Enabled && r.MCP != nil {
			for _, b := range r.MCP.Bindings {
				available[b.Name] = true
			}
		}
	}
	var out strings.Builder
	out.WriteString("以下是我显式选择的调查参考，请结合当前证据使用。它们不授予额外工具或执行权限。\n\n")
	seen := map[string]bool{}
	for _, ref := range in.References {
		if ref.Version < 1 || seen[ref.ID] {
			return Composed{}, ErrInvalid
		}
		seen[ref.ID] = true
		current, e := s.Repository.Get(ctx, ref.ID, 0)
		if e != nil {
			return Composed{}, e
		}
		if (current.ApplicationID != "" && current.ApplicationID != in.ApplicationID) || !current.Enabled || (current.Kind != "skill" && current.Kind != "prompt") {
			return Composed{}, store.ErrForbidden
		}
		r, e := s.Repository.Get(ctx, ref.ID, ref.Version)
		if e != nil {
			return Composed{}, e
		}
		for _, t := range r.RequiredTools {
			if !available[t] {
				return Composed{}, fmt.Errorf("%w: required tool %s is not configured", ErrInvalid, t)
			}
		}
		fmt.Fprintf(&out, "[aiops-extension kind=%s id=%s version=%d sha256=%s]\n标题：%s\n维护人：%s\n%s\n[/aiops-extension]\n\n", r.Kind, r.ID, r.Version, r.ContentHash, r.Title, r.Maintainer, r.Body)
	}
	out.WriteString("本轮问题：\n" + strings.TrimSpace(in.Question))
	if out.Len() > 48000 {
		return Composed{}, ErrInvalid
	}
	return Composed{Content: out.String(), References: in.References}, nil
}

func Examples() []Record {
	return []Record{
		{Kind: "skill", Title: "SRE 只读取证", Maintainer: "平台运维", Enabled: true, RequiredTools: []string{}, Body: "确认应用与时间窗口。先查服务健康、错误率、延迟、日志及最近变更。分别列出已知事实、待验证假设和缺失证据；引用来源与采集时间。只提出恢复方案，写操作必须人工批准。"},
		{Kind: "prompt", Title: "日志异常对比", Maintainer: "平台运维", Enabled: true, RequiredTools: []string{}, Body: "对比异常前后的日志模式，区分新错误、持续错误与流量变化。按证据列出影响范围、错误示例及下一步只读查询。不要把缺少日志解释为服务健康。"},
		{Kind: "prompt", Title: "发布变更与故障关联", Maintainer: "平台运维", Enabled: true, RequiredTools: []string{}, Body: "核对故障时间线与部署版本、配置变更及发布记录。时间上的相关性不等于根因；提出一个可证伪的验证步骤，并明确回滚风险和验证条件。"},
	}
}
