package v3catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type mappingSaveRequest struct {
	EnvironmentID string                 `json:"environment_id"`
	Items         []mappingSaveInputItem `json:"items"`
}

type mappingSaveInputItem struct {
	ServiceID            string                `json:"service_id"`
	ServiceEnvironmentID string                `json:"service_environment_id"`
	BindingRevision      int64                 `json:"binding_revision"`
	Bindings             *[]mappingSaveBinding `json:"bindings"`
}

type mappingSaveCommittedItem struct {
	ServiceID            string               `json:"service_id"`
	ServiceEnvironmentID string               `json:"service_environment_id"`
	BindingRevision      int64                `json:"binding_revision"`
	Bindings             []mappingSaveBinding `json:"bindings"`
}

// The shared decoder accepts duplicate JSON names and case-insensitive field
// aliases. A replacement write cannot let either shape silently override an
// identity, revision or bindings array, so this route checks its small exact
// JSON grammar before decoding the typed request.
func decodeMappingSave(w http.ResponseWriter, r *http.Request) (mappingSaveRequest, bool) {
	var input mappingSaveRequest
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		httpapi.Error(w, 422, "invalid_body", "Invalid mapping save body")
		return input, false
	}
	check := json.NewDecoder(bytes.NewReader(body))
	check.UseNumber()
	if err := checkMappingSaveJSON(check, "request"); err != nil {
		httpapi.Error(w, 422, "invalid_body", "Invalid mapping save fields")
		return input, false
	}
	if _, err := check.Token(); !errors.Is(err, io.EOF) {
		httpapi.Error(w, 422, "invalid_body", "Expected one mapping save object")
		return input, false
	}
	if err := json.Unmarshal(body, &input); err != nil {
		httpapi.Error(w, 422, "invalid_body", "Invalid mapping save values")
		return input, false
	}
	return input, true
}

func checkMappingSaveJSON(decoder *json.Decoder, shape string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if shape == "string" {
		if _, ok := token.(string); !ok {
			return errors.New("expected string")
		}
		return nil
	}
	if shape == "revision" {
		if _, ok := token.(json.Number); !ok {
			return errors.New("expected integer")
		}
		return nil
	}
	if shape == "bool" {
		if _, ok := token.(bool); !ok {
			return errors.New("expected boolean")
		}
		return nil
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return errors.New("expected object or array")
	}
	switch shape {
	case "items", "bindings":
		if delim != '[' {
			return errors.New("expected array")
		}
		child := "item"
		if shape == "bindings" {
			child = "binding"
		}
		for decoder.More() {
			if err := checkMappingSaveJSON(decoder, child); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case "request", "item", "binding":
		if delim != '{' {
			return errors.New("expected object")
		}
		fields := map[string]string{}
		switch shape {
		case "request":
			fields = map[string]string{"environment_id": "string", "items": "items"}
		case "item":
			// editable and unavailable_reason come with the GET save snapshot;
			// a snapshot sent back as it is is accepted and they are ignored.
			fields = map[string]string{"service_id": "string", "service_environment_id": "string",
				"binding_revision": "revision", "bindings": "bindings", "editable": "bool", "unavailable_reason": "string"}
		case "binding":
			fields = map[string]string{"system_id": "string", "kind": "string",
				"connector_id": "string", "resource": "string"}
		}
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] || fields[key] == "" {
				return errors.New("duplicate or unknown field")
			}
			seen[key] = true
			if err := checkMappingSaveJSON(decoder, fields[key]); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid JSON shape")
	}
}

func validateMappingSave(input mappingSaveRequest, selectedEnvironment string) ([]mappingSaveInputItem, error) {
	if input.EnvironmentID != selectedEnvironment || len(input.Items) < 1 || len(input.Items) > 50 {
		return nil, errors.New("choose one environment and 1 to 50 service mappings")
	}
	seenServices := map[string]bool{}
	for _, item := range input.Items {
		if !mappingSaveIdentity(item.ServiceID) || !mappingSaveIdentity(item.ServiceEnvironmentID) ||
			item.BindingRevision < 1 || item.Bindings == nil || len(*item.Bindings) > len(mappingSystems) {
			return nil, errors.New("service identity, revision and explicit bindings array are required")
		}
		if seenServices[item.ServiceID] {
			return nil, errors.New("duplicate service identity")
		}
		seenServices[item.ServiceID] = true
		seenSystems := map[string]bool{}
		for _, binding := range *item.Bindings {
			if mappingKind(binding.Kind) != binding.SystemID || binding.SystemID == "" ||
				(binding.SystemID == "code" && binding.Kind != "gitlab") ||
				!mappingSaveIdentity(binding.Kind) || !mappingSaveIdentity(binding.ConnectorID) ||
				!mappingSaveResource(binding.Resource) || seenSystems[binding.SystemID] {
				return nil, errors.New("invalid or duplicate mapping binding")
			}
			seenSystems[binding.SystemID] = true
		}
	}
	sort.Slice(input.Items, func(i, j int) bool { return input.Items[i].ServiceID < input.Items[j].ServiceID })
	return input.Items, nil
}

func mappingSaveIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t ")
}

func mappingSaveResource(value string) bool {
	return value != "" && len(value) <= 2048 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func saveResponseBindings(persisted []core.Binding) []mappingSaveBinding {
	bindings := make([]mappingSaveBinding, 0, len(persisted))
	for _, binding := range persisted {
		bindings = append(bindings, mappingSaveBinding{SystemID: mappingKind(binding.Kind),
			Kind: binding.Kind, ConnectorID: binding.ConnectorID, Resource: binding.Resource})
	}
	return bindings
}

func (s *Service) mappingSave(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "change")
	if !ok {
		return
	}
	input, ok := decodeMappingSave(w, r)
	if !ok {
		return
	}
	items, err := validateMappingSave(input, envs[0])
	if err != nil {
		httpapi.Error(w, 422, "invalid_mappings", err.Error())
		return
	}
	access, ok := s.transactionAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return
	}
	defer tx.Rollback(ctx)
	user := httpapi.CurrentUser(r)
	// Admin/account writers take these advisory locks exclusively. Hold the
	// corresponding shared locks through commit so a completed revocation
	// cannot race the final grant recheck below.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
			return
		}
	}
	for _, action := range []string{"view", "manage"} {
		if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
			policyError(w, err)
			return
		}
	}
	// Precheck every object before taking write locks. Hidden and missing
	// services have the same response; a batch cannot probe another team.
	for _, item := range items {
		appID, teamID, _, _, _, err := mappingSaveTarget(ctx, tx, item.ServiceID, envs[0], false)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Service mapping not found")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return
		}
		for _, action := range []string{"view", "change"} {
			if err := authorizeMappingService(ctx, access, tx, user, appID, envs[0], teamID, action); err != nil {
				mappingSavePolicyError(w, err)
				return
			}
		}
	}
	// Lock in the same service-first order as candidate acceptance, then
	// recheck identities, grants and revisions under the locks.
	for _, item := range items {
		if _, _, _, _, _, err := mappingSaveTarget(ctx, tx, item.ServiceID, envs[0], true); err != nil {
			mappingSaveTargetError(w, err)
			return
		}
	}
	committed := make([]mappingSaveCommittedItem, 0, len(items))
	for _, item := range items {
		appID, teamID, envID, raw, revision, err := mappingSaveTarget(ctx, tx, item.ServiceID, envs[0], true)
		if err != nil {
			mappingSaveTargetError(w, err)
			return
		}
		for _, action := range []string{"view", "change"} {
			if err := authorizeMappingService(ctx, access, tx, user, appID, envs[0], teamID, action); err != nil {
				mappingSavePolicyError(w, err)
				return
			}
		}
		for _, action := range []string{"view", "manage"} {
			if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
				policyError(w, err)
				return
			}
		}
		// Registry systems are written as targets (mapping_write.go).
		saved, ok := s.registryMappingWrite(ctx, tx, w, user, mappingCatalogRow{appID: appID, env: envs[0], envID: envID,
			bindings: raw, revision: revision}, item)
		if !ok {
			return
		}
		committed = append(committed, saved)
	}
	if err := lockMappingWriteSession(ctx, tx, r, user.ID); err != nil {
		policyError(w, err)
		return
	}
	// Recheck at the transaction tail, after any wait and before committing
	// the bindings and audit together.
	for _, action := range []string{"view", "manage"} {
		if err := authorizeMappingConnector(ctx, access, tx, user, envs[0], action); err != nil {
			policyError(w, err)
			return
		}
	}
	for _, item := range items {
		appID, teamID, envID, _, _, err := mappingSaveTarget(ctx, tx, item.ServiceID, envs[0], false)
		if err != nil {
			mappingSaveTargetError(w, err)
			return
		}
		if envID != item.ServiceEnvironmentID {
			httpapi.Error(w, 409, "stale_mapping", "Mapping environment identity changed")
			return
		}
		for _, action := range []string{"view", "change"} {
			if err := authorizeMappingService(ctx, access, tx, user, appID, envs[0], teamID, action); err != nil {
				mappingSavePolicyError(w, err)
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping commit failed")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"environment_id": envs[0], "items": committed})
}

func mappingSaveTarget(ctx context.Context, tx pgx.Tx, serviceID, environment string, lock bool) (string, string, string, []byte, int64, error) {
	query := `SELECT a.id,COALESCE(a.owner_team_id,''),e.id,e.bindings,e.binding_revision
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`
	if lock {
		query += ` FOR UPDATE OF a,ve,e`
	}
	var appID, teamID, envID string
	var raw []byte
	var revision int64
	err := tx.QueryRow(ctx, query, serviceID, environment).Scan(&appID, &teamID, &envID, &raw, &revision)
	return appID, teamID, envID, raw, revision, err
}

func mappingSaveTargetError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Service mapping not found")
		return
	}
	httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
}

func mappingSavePolicyError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 404, "not_found", "Service mapping not found")
		return
	}
	policyError(w, err)
}
