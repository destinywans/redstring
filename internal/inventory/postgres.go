package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// SaveSnapshot commits only complete source snapshots. Failed/old observations
// retain the previous objects, edges and observed_at; missing objects are marked,
// never deleted. Explicit grants use existing permissions and only action=view.
func SaveSnapshot(ctx context.Context, db *pgxpool.Pool, snapshot Snapshot) error {
	if snapshot.SourceID == "" || snapshot.SourceKind == "" {
		return fmt.Errorf("source identity required")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,109))`, snapshot.SourceKind+":"+snapshot.SourceID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO cmdb_sync_sources(id,kind,name,last_attempt_at) VALUES($1,$2,$3,now()) ON CONFLICT(id) DO UPDATE SET last_attempt_at=now(),updated_at=now()`, snapshot.SourceID, snapshot.SourceKind, snapshot.SourceName)
	if err != nil {
		return err
	}
	var sourceKind string
	if err = tx.QueryRow(ctx, `SELECT kind FROM cmdb_sync_sources WHERE id=$1`, snapshot.SourceID).Scan(&sourceKind); err != nil {
		return err
	}
	if sourceKind != snapshot.SourceKind {
		return fmt.Errorf("inventory source kind is immutable")
	}
	if snapshot.Error != "" {
		_, err = tx.Exec(ctx, `UPDATE cmdb_sync_sources SET last_error=$2 WHERE id=$1`, snapshot.SourceID, snapshot.Error)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("valid observation time required")
	}
	var newer bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(last_success_at >= $2 AND last_success_at <= now()+interval '1 minute',false) FROM cmdb_sync_sources WHERE id=$1`, snapshot.SourceID, snapshot.ObservedAt).Scan(&newer); err != nil {
		return err
	}
	if newer {
		return tx.Commit(ctx)
	}
	if len(snapshot.Resources) > 10000 {
		return fmt.Errorf("source resource limit exceeded")
	}
	ids := map[string]string{}
	for _, record := range snapshot.Resources {
		if record.ExternalID == "" || record.Name == "" || record.Kind == "" {
			return fmt.Errorf("resource identity required")
		}
		if _, ok := ids[record.ExternalID]; ok {
			return fmt.Errorf("duplicate source UID")
		}
		id := ResourceID(snapshot.SourceKind, snapshot.SourceID, record.ExternalID)
		ids[record.ExternalID] = id
		if record.Attributes == nil {
			record.Attributes = map[string]any{}
		}
		record.Attributes["inventory_presence"] = "present"
		observed := record.ObservedAt
		if observed.IsZero() {
			observed = snapshot.ObservedAt
		}
		if observed.After(snapshot.ObservedAt.Add(time.Minute)) {
			return fmt.Errorf("resource observation newer than snapshot")
		}
		attrs, e := json.Marshal(record.Attributes)
		if e != nil {
			return e
		}
		if len(attrs) > 131072 {
			return fmt.Errorf("resource attributes too large")
		}
		inserted := false
		err = tx.QueryRow(ctx, `INSERT INTO cmdb_resources(id,kind,name,description,external_id,source_kind,source_id,attributes,is_mock,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,false,$9)
 ON CONFLICT(source_kind,source_id,external_id) WHERE external_id<>'' DO UPDATE SET kind=excluded.kind,name=excluded.name,description=excluded.description,attributes=excluded.attributes,is_mock=false,observed_at=excluded.observed_at,updated_at=now() RETURNING id,(xmax=0)`, id, record.Kind, record.Name, record.Description, record.ExternalID, snapshot.SourceKind, snapshot.SourceID, attrs, observed).Scan(&id, &inserted)
		if err != nil {
			return err
		}
		ids[record.ExternalID] = id
		if _, err = tx.Exec(ctx, `DELETE FROM cmdb_resource_applications WHERE resource_id=$1`, id); err != nil {
			return err
		}
		for _, link := range record.Applications {
			_, err = tx.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, link.ApplicationID, link.EnvironmentID)
			if err != nil {
				return err
			}
		}
		// Granting an explicit permission does not attach that permission to any role.
		if inserted && record.Kind != "pod" && record.Kind != "event" {
			for _, permission := range snapshot.GrantPermissionIDs {
				var valid bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_permissions WHERE id=$1) AND rbac_is_infrastructure_kind($2)`, permission, record.Kind).Scan(&valid); err != nil {
					return err
				}
				if !valid {
					continue
				}
				result, grantErr := tx.Exec(ctx, `INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES($1,'resource',$2,'view') ON CONFLICT DO NOTHING`, permission, id)
				if grantErr != nil {
					return grantErr
				}
				if result.RowsAffected() > 0 {
					if _, err = tx.Exec(ctx, `UPDATE rbac_permissions SET revision=revision+1 WHERE id=$1`, permission); err != nil {
						return err
					}
				}
			}
		}
	}
	seen := make([]string, 0, len(ids))
	for _, id := range ids {
		seen = append(seen, id)
	}
	_, err = tx.Exec(ctx, `UPDATE cmdb_resources SET attributes=jsonb_set(attributes,'{inventory_presence}','"missing"'),updated_at=now() WHERE source_kind=$1 AND source_id=$2 AND NOT(id=ANY($3::text[])) AND attributes->>'inventory_presence' IS DISTINCT FROM 'missing'`, snapshot.SourceKind, snapshot.SourceID, seen)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM cmdb_resource_relations WHERE source_kind=$1 AND source_id=$2 AND provenance='discovered'`, snapshot.SourceKind, snapshot.SourceID); err != nil {
		return err
	}
	for _, edge := range snapshot.Relations {
		a, b := ids[edge.SourceExternalID], ids[edge.TargetExternalID]
		if a == "" || b == "" || a == b {
			continue
		}
		id := ResourceID(snapshot.SourceKind, snapshot.SourceID, a+":"+edge.Kind+":"+b)
		_, err = tx.Exec(ctx, `INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id,is_mock) VALUES($1,$2,$3,$4,'discovered',$5,$6,false) ON CONFLICT DO NOTHING`, id, a, b, edge.Kind, snapshot.SourceKind, snapshot.SourceID)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE cmdb_sync_sources SET kind=$2,name=$3,last_success_at=$4,last_error='',resource_count=$5,updated_at=now() WHERE id=$1`, snapshot.SourceID, snapshot.SourceKind, snapshot.SourceName, snapshot.ObservedAt, len(ids))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
