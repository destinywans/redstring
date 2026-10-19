package v3canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type templateRetention struct {
	Steps         int    `json:"steps"`
	Runbooks      int    `json:"runbooks"`
	ContentBlocks int    `json:"content_blocks"`
	RunbookSource string `json:"runbook_source,omitempty"`
}

type privateTemplateRunbookRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
}

// This metadata lives only in the first private template node. Normal canvas
// decoding drops it, while the strict configuration exporter rejects it.
type privateTemplateMetadata struct {
	Steps          bool                        `json:"steps,omitempty"`
	Content        bool                        `json:"content,omitempty"`
	Runbooks       []privateTemplateRunbookRef `json:"runbooks,omitempty"`
	SourceServices []string                    `json:"source_services"`
}

var (
	errTemplateRunbookUnavailable = errors.New("template runbook source unavailable")
	errTemplateRunbookForbidden   = errors.New("template runbook access denied")
	errTemplateRunbookInvalid     = errors.New("template runbook reference invalid")
	runbookArtifactID             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,199}$`)
)

const pendingTemplateStepPrefix = "模板步骤（未执行，待复核）· "

func blankRetainedTemplateNode() Node {
	return Node{ID: "node-1", X: 16, Y: 140, W: 260, Icon: "layers", Tone: "info",
		Title: "节点 1", Sub: "", Blocks: []json.RawMessage{}}
}

func checkTemplateSourceServices(ctx context.Context, tx pgx.Tx, st *store.Store, user core.User,
	env string, services []string) *creationFailure {
	if !validStrings(services) {
		return &creationFailure{409, "template_source_changed", "Source canvas service bindings are invalid"}
	}
	policy := &rbac.V3Policy{Store: st}
	for _, app := range services {
		var active string
		err := tx.QueryRow(ctx, `SELECT a.id FROM applications a
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key=$2 AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ae,vae`, app, env).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return &creationFailure{409, "template_source_changed", "Source canvas service is no longer active"}
		}
		if err != nil {
			return creationUnavailable()
		}
		err = policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
			Module: "canvas", Action: "change", EnvironmentID: env, ApplicationID: app})
		if errors.Is(err, store.ErrForbidden) {
			return &creationFailure{403, "forbidden", "Current source service access is required"}
		}
		if err != nil {
			return creationUnavailable()
		}
	}
	return nil
}

// Content is a deliberate private snapshot of durable node text, not an
// automatic redaction. Bars and current row values/states are live observations
// and never become reusable sample content. Steps are handled separately.
func retainTemplateNodes(source []Node, sourceEdges []Edge, keep templateKeepOptions) ([]Node, []Edge, int, int, error) {
	nodes, edges, err := sanitizeTemplateStructure(source, sourceEdges)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	if !*keep.Nodes {
		edges = []Edge{}
	}
	retained := make([]Node, 0, len(nodes))
	stepCount, contentCount := 0, 0
	for i, src := range source {
		n := nodes[i]
		if keep.Content {
			n.Title, n.Sub = src.Title, src.Sub
		}
		for _, raw := range src.Blocks {
			var tag struct {
				Type string `json:"type"`
			}
			if err = json.Unmarshal(raw, &tag); err != nil {
				return nil, nil, 0, 0, err
			}
			switch tag.Type {
			case "steps":
				if !keep.Steps {
					continue
				}
				clean, count, stepErr := pendingTemplateSteps(raw)
				if stepErr != nil {
					return nil, nil, 0, 0, stepErr
				}
				n.Blocks = append(n.Blocks, clean)
				stepCount += count
			case "text", "tags", "kv", "callout":
				if keep.Content {
					n.Blocks = append(n.Blocks, raw)
					contentCount++
				}
			case "rows":
				if keep.Content {
					var block struct {
						Items []canvasRow `json:"items"`
					}
					if err = json.Unmarshal(raw, &block); err != nil {
						return nil, nil, 0, 0, err
					}
					texts := make([]canvasRow, 0, len(block.Items))
					for _, row := range block.Items {
						texts = append(texts, canvasRow{Text: row.Text})
					}
					n.Blocks = append(n.Blocks, mustJSON(map[string]any{"type": "rows", "items": texts}))
					contentCount++
				}
			case "bars":
				// Sampled metric values cannot become current observations.
			default:
				return nil, nil, 0, 0, errors.New("unsupported source block")
			}
		}
		if !*keep.Nodes {
			if !keep.Content && len(n.Blocks) == 0 {
				continue
			}
			n.ID = fmt.Sprintf("node-%d", len(retained)+1)
			n.X, n.Y, n.W = 16, 140+len(retained)*160, 260
		}
		retained = append(retained, n)
	}
	return retained, edges, stepCount, contentCount, nil
}

func pendingTemplateSteps(raw json.RawMessage) (json.RawMessage, int, error) {
	var block struct {
		Type  string       `json:"type"`
		Items []canvasStep `json:"items"`
	}
	if err := json.Unmarshal(raw, &block); err != nil || block.Type != "steps" {
		return nil, 0, errors.New("invalid retained steps")
	}
	for i := range block.Items {
		if block.Items[i].State == "done" {
			block.Items[i].State = "todo"
		}
		if !strings.HasPrefix(block.Items[i].Sub, pendingTemplateStepPrefix) {
			if len(pendingTemplateStepPrefix)+len(block.Items[i].Sub) > maxCanvasText {
				return nil, 0, errors.New("retained step description exceeds limit")
			}
			block.Items[i].Sub = pendingTemplateStepPrefix + block.Items[i].Sub
		}
	}
	return mustJSON(block), len(block.Items), nil
}

func resetLoadedTemplateSteps(nodes []Node) error {
	for i := range nodes {
		for j, raw := range nodes[i].Blocks {
			var tag struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(raw, &tag); err != nil {
				return err
			}
			if tag.Type == "steps" {
				clean, _, err := pendingTemplateSteps(raw)
				if err != nil {
					return err
				}
				nodes[i].Blocks[j] = clean
			}
		}
	}
	return nil
}

func runbookIDFromArtifact(href string) (string, bool, error) {
	if !strings.HasPrefix(href, "/runbooks/") {
		return "", false, nil
	}
	id := strings.TrimPrefix(href, "/runbooks/")
	if !runbookArtifactID.MatchString(id) {
		return "", true, errTemplateRunbookInvalid
	}
	return id, true, nil
}

func collectPrivateRunbookRefs(ctx context.Context, tx pgx.Tx, st *store.Store, user core.User,
	env string, services []string, artifacts []CanvasArtifact) ([]privateTemplateRunbookRef, error) {
	refs := []privateTemplateRunbookRef{}
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		id, linked, err := runbookIDFromArtifact(artifact.Href)
		if err != nil {
			return nil, err
		}
		if !linked || seen[id] {
			continue
		}
		_, version, err := checkPrivateTemplateRunbook(ctx, tx, st, user, env, services, id, 0)
		if err != nil {
			return nil, err
		}
		seen[id] = true
		refs = append(refs, privateTemplateRunbookRef{ID: id, Version: version, X: artifact.X, Y: artifact.Y})
	}
	return refs, nil
}

func checkPrivateTemplateRunbook(ctx context.Context, tx pgx.Tx, st *store.Store, user core.User,
	env string, services []string, id string, expectedVersion int64) (string, int64, error) {
	var title, source, lifecycle, app, team string
	var version int64
	var metadataRaw []byte
	err := tx.QueryRow(ctx, `SELECT d.title,d.version,d.source,d.lifecycle,d.application_id,
 d.metadata,COALESCE(a.owner_team_id,'') FROM knowledge_documents d
 JOIN v3_runbooks vr ON vr.document_id=d.id
 JOIN applications a ON a.id=d.application_id AND NOT a.archived
 JOIN application_environments ae ON ae.application_id=a.id AND ae.key=$2 AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
 WHERE d.id=$1 FOR SHARE OF d,vr,a,ae,vae`, id, env).
		Scan(&title, &version, &source, &lifecycle, &app, &metadataRaw, &team)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, errTemplateRunbookUnavailable
	}
	if err != nil {
		return "", 0, err
	}
	bound := false
	for _, service := range services {
		if service == app {
			bound = true
			break
		}
	}
	var metadata knowledge.Metadata
	var raw map[string]json.RawMessage
	if json.Unmarshal(metadataRaw, &metadata) != nil || json.Unmarshal(metadataRaw, &raw) != nil ||
		raw == nil || raw["source_registration_id"] != nil ||
		!strings.HasPrefix(source, "runbook://") || lifecycle != "published" ||
		version <= 0 || expectedVersion > 0 && version != expectedVersion ||
		!bound || metadata.Visibility != "application" ||
		!containsTemplateEnvironment(metadata.EnvironmentIDs, env) ||
		metadata.TeamID != "" && metadata.TeamID != team ||
		len(title) > maxCanvasText {
		return "", 0, errTemplateRunbookUnavailable
	}
	for _, candidate := range metadata.EnvironmentIDs {
		if strings.TrimSpace(candidate) == "" {
			return "", 0, errTemplateRunbookUnavailable
		}
	}
	if _, present := raw["team_id"]; present && metadata.TeamID == "" {
		return "", 0, errTemplateRunbookUnavailable
	}
	policy := &rbac.V3Policy{Store: st}
	decisions := []rbac.V3Decision{
		{Module: "environment", Action: "view", EnvironmentID: env},
		{Module: "knowledge", Action: "view", EnvironmentID: env, ApplicationID: app},
	}
	if metadata.TeamID != "" {
		decisions = append(decisions, rbac.V3Decision{
			Module: "knowledge", Action: "view", EnvironmentID: env, TeamID: team})
	}
	for _, decision := range decisions {
		if err = policy.AuthorizeWithTx(ctx, tx, user, decision); errors.Is(err, store.ErrForbidden) {
			return "", 0, errTemplateRunbookForbidden
		} else if err != nil {
			return "", 0, err
		}
	}
	return title, version, nil
}

func templateRunbookFailure(err error) *creationFailure {
	switch {
	case errors.Is(err, errTemplateRunbookForbidden):
		return &creationFailure{403, "template_reference_forbidden", "Current Runbook access is required"}
	case errors.Is(err, errTemplateRunbookInvalid):
		return &creationFailure{422, "invalid_template_reference", "Runbook link is invalid"}
	case errors.Is(err, errTemplateRunbookUnavailable):
		return &creationFailure{409, "template_runbook_unavailable", "Runbook publication, version or scope changed"}
	default:
		return creationUnavailable()
	}
}

func marshalPrivateTemplateNodes(nodes []Node, metadata privateTemplateMetadata) ([]byte, error) {
	if len(nodes) == 0 || !metadata.Steps && !metadata.Content && len(metadata.Runbooks) == 0 {
		return json.Marshal(nodes)
	}
	first := struct {
		Node
		Private privateTemplateMetadata `json:"private_template_v1"`
	}{nodes[0], metadata}
	encoded := make([]json.RawMessage, len(nodes))
	var err error
	if encoded[0], err = json.Marshal(first); err != nil {
		return nil, err
	}
	for i := 1; i < len(nodes); i++ {
		if encoded[i], err = json.Marshal(nodes[i]); err != nil {
			return nil, err
		}
	}
	return json.Marshal(encoded)
}

func decodePrivateTemplateMetadata(rawNodes []byte, private bool) (privateTemplateMetadata, error) {
	var nodes []json.RawMessage
	if err := json.Unmarshal(rawNodes, &nodes); err != nil {
		return privateTemplateMetadata{}, err
	}
	metadata := privateTemplateMetadata{}
	for i, raw := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			return metadata, err
		}
		metaRaw, present := node["private_template_v1"]
		if !present {
			continue
		}
		if !private || i != 0 || len(metaRaw) > 32<<10 {
			return metadata, errTemplateRunbookInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(metaRaw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&metadata); err != nil || decoder.Decode(new(any)) != io.EOF ||
			!metadata.Steps && !metadata.Content && len(metadata.Runbooks) == 0 ||
			len(metadata.Runbooks) > maxCanvasArtifacts || metadata.SourceServices == nil ||
			!validStrings(metadata.SourceServices) {
			return metadata, errTemplateRunbookInvalid
		}
		seen := map[string]bool{}
		for _, ref := range metadata.Runbooks {
			if !runbookArtifactID.MatchString(ref.ID) || ref.Version <= 0 || seen[ref.ID] ||
				!canvasCoordinate(ref.X) || !canvasCoordinate(ref.Y) {
				return metadata, errTemplateRunbookInvalid
			}
			seen[ref.ID] = true
		}
	}
	return metadata, nil
}
