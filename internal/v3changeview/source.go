package v3changeview

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/delivery"
)

const maxRuns = 500
const maxEvents = 500
const maxAlerts = 200

type runRecord struct {
	Run   delivery.Run
	Actor string
}

type deploymentRecord struct {
	ExecutionID, RunID, PipelineDigest, ArtifactDigest string
	At                                                 time.Time
	Parent                                             delivery.Run
}

type alertRecord struct {
	ID, Title, Status string
	StartsAt          time.Time
	EndsAt            *time.Time
}

func (s *Service) loadService(ctx context.Context, app, env string) (serviceRow, error) {
	var row serviceRow
	err := s.Store.DB.QueryRow(ctx, `SELECT a.id,e.id,e.key,a.key,a.name,COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`, app, env).
		Scan(&row.AppID, &row.EnvID, &row.EnvKey, &row.Key, &row.Name, &row.TeamID)
	return row, err
}

func (s *Service) loadRuns(ctx context.Context, row serviceRow, start, end time.Time) ([]runRecord, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT r.id,r.pipeline_id,r.version,r.application_id,r.environment_id,r.provider,
 r.status,r.external_id,r.snapshot,r.projection,r.created_by,r.created_at,r.updated_at,
 COALESCE(u.name,'')
 FROM delivery_runs r JOIN users u ON u.id=r.created_by
 WHERE r.application_id=$1 AND r.environment_id=$2 AND r.provider='native'
 AND r.created_at>=$3 AND r.created_at<$4
 ORDER BY r.created_at DESC,r.id DESC LIMIT 501`, row.AppID, row.EnvID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []runRecord{}
	for rows.Next() {
		var item runRecord
		var snapshot, projection []byte
		err = rows.Scan(&item.Run.ID, &item.Run.PipelineID, &item.Run.Version,
			&item.Run.ApplicationID, &item.Run.EnvironmentID, &item.Run.Provider,
			&item.Run.Status, &item.Run.ExternalID, &snapshot, &projection,
			&item.Run.CreatedBy, &item.Run.CreatedAt, &item.Run.UpdatedAt, &item.Actor)
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(snapshot, &item.Run.Snapshot) != nil || json.Unmarshal(projection, &item.Run.Projection) != nil {
			return nil, errIncomplete
		}
		out = append(out, item)
		if len(out) > maxRuns {
			return nil, errLimit
		}
	}
	return out, rows.Err()
}

// Successful GitOps events are only counted when their verified release source
// still names one of this window's exact native runs and its immutable digest.
func (s *Service) loadDeployments(ctx context.Context, row serviceRow, start, end time.Time,
	runs []runRecord) ([]deploymentRecord, error) {
	byRun := make(map[string]delivery.Run, len(runs))
	for _, item := range runs {
		byRun[item.Run.ID] = item.Run
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT x.id,x.status,
 p.snapshot#>>'{source,run_id}',p.snapshot#>>'{source,pipeline_digest}',
 p.snapshot#>>'{source,artifact_digest}',de.created_at,
 p.snapshot#>>'{source,application_id}',p.snapshot#>>'{source,environment_id}',
 r.id,r.pipeline_id,r.version,r.application_id,r.environment_id,r.provider,
 r.status,r.external_id,r.snapshot,r.projection,r.created_by,r.created_at,r.updated_at
 FROM deployment_events de
 JOIN deployment_executions x ON x.id=de.execution_id
 JOIN deployment_plans p ON p.id=x.plan_id
 JOIN delivery_gates g ON g.plan_id=p.id AND g.execution_id=x.id
 AND g.run_id=p.snapshot#>>'{source,run_id}'
 JOIN delivery_runs r ON r.id=g.run_id
 JOIN application_environments e ON e.id=p.environment_id AND e.application_id=p.application_id AND NOT e.archived
 WHERE p.application_id=$1 AND e.key=$2 AND de.type='succeeded'
 AND p.snapshot->>'target_kind'='gitops' AND de.created_at>=$3 AND de.created_at<$4
 ORDER BY de.created_at,de.sequence LIMIT 501`, row.AppID, row.EnvKey, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []deploymentRecord{}
	seen := map[string]bool{}
	for rows.Next() {
		var item deploymentRecord
		var executionStatus string
		var sourceApp, sourceEnv *string
		var snapshot, projection []byte
		err = rows.Scan(&item.ExecutionID, &executionStatus, &item.RunID, &item.PipelineDigest,
			&item.ArtifactDigest, &item.At, &sourceApp, &sourceEnv,
			&item.Parent.ID, &item.Parent.PipelineID, &item.Parent.Version,
			&item.Parent.ApplicationID, &item.Parent.EnvironmentID, &item.Parent.Provider,
			&item.Parent.Status, &item.Parent.ExternalID, &snapshot, &projection,
			&item.Parent.CreatedBy, &item.Parent.CreatedAt, &item.Parent.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(snapshot, &item.Parent.Snapshot) != nil || json.Unmarshal(projection, &item.Parent.Projection) != nil {
			return nil, errIncomplete
		}
		if err = validateRun(item.Parent); err != nil {
			return nil, err
		}
		if _, err = nodeStates(item.Parent); err != nil {
			return nil, err
		}
		if run, ok := byRun[item.RunID]; ok && !reflect.DeepEqual(run, item.Parent) {
			return nil, errIncomplete
		}
		if item.Parent.ID != item.RunID || item.Parent.ApplicationID != row.AppID || item.Parent.EnvironmentID != row.EnvID ||
			item.Parent.Status != "succeeded" || executionStatus != "succeeded" || sourceApp == nil || sourceEnv == nil || *sourceApp != row.AppID ||
			*sourceEnv != row.EnvID || item.PipelineDigest == "" || item.ArtifactDigest == "" ||
			item.PipelineDigest != item.Parent.Snapshot.Digest || seen[item.ExecutionID] ||
			item.At.Before(item.Parent.CreatedAt) {
			return nil, errIncomplete
		}
		seen[item.ExecutionID] = true
		out = append(out, item)
		if len(out) > maxEvents {
			return nil, errLimit
		}
	}
	return out, rows.Err()
}

func (s *Service) loadAlerts(ctx context.Context, row serviceRow, start, end time.Time) ([]alertRecord, error) {
	if !s.AlertCovered(row.AppID, row.EnvKey, start, end) {
		return nil, errIncomplete
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT i.id,i.title,i.alert_status,i.starts_at,i.ends_at
 FROM ops_incidents i
 WHERE i.application_id=$1 AND i.environment_id=$2 AND NOT i.suspected
 AND i.starts_at<$4 AND (i.ends_at IS NULL OR i.ends_at>$3)
 ORDER BY i.starts_at,i.id LIMIT 201`, row.AppID, row.EnvID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []alertRecord{}
	for rows.Next() {
		var item alertRecord
		if err = rows.Scan(&item.ID, &item.Title, &item.Status, &item.StartsAt, &item.EndsAt); err != nil {
			return nil, err
		}
		if (item.Status != "firing" && item.Status != "resolved") ||
			(item.Status == "firing" && item.EndsAt != nil) ||
			(item.Status == "resolved" && item.EndsAt == nil) ||
			(item.EndsAt != nil && item.EndsAt.Before(item.StartsAt)) {
			return nil, errIncomplete
		}
		out = append(out, item)
		if len(out) > maxAlerts {
			return nil, errLimit
		}
	}
	return out, rows.Err()
}

func findRun(rows []runRecord, id string) (runRecord, error) {
	for _, row := range rows {
		if row.Run.ID == id {
			return row, nil
		}
	}
	return runRecord{}, pgx.ErrNoRows
}

func findAlert(rows []alertRecord, id string) (alertRecord, error) {
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return alertRecord{}, errIncomplete
}

func selectedDeployment(rows []deploymentRecord, runID, executionID string) (deploymentRecord, error) {
	for _, row := range rows {
		if row.RunID == runID && row.ExecutionID == executionID {
			return row, nil
		}
	}
	return deploymentRecord{}, errIncomplete
}

func sameRunPopulation(a, b []runRecord) bool {
	return reflect.DeepEqual(a, b)
}

func sameDeployments(a, b []deploymentRecord) bool {
	return reflect.DeepEqual(a, b)
}

func sameAlerts(a, b []alertRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Title != b[i].Title || a[i].Status != b[i].Status || !a[i].StartsAt.Equal(b[i].StartsAt) ||
			(a[i].EndsAt == nil) != (b[i].EndsAt == nil) {
			return false
		}
		if a[i].EndsAt != nil && !a[i].EndsAt.Equal(*b[i].EndsAt) {
			return false
		}
	}
	return true
}

func sourceErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errIncomplete
	}
	return err
}
