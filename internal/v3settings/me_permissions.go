package v3settings

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Reach of an effective grant in one environment (PA-64):
//   - all: a global or environment-wide grant covers every object here;
//   - scoped: only some applications or teams are covered, so the object
//     itself decides and a write may still be refused;
//   - conditional: approve, which also needs a human identity, a passkey
//     ceremony and the proposal's own checks when it is used.
const (
	reachAll         = "all"
	reachScoped      = "scoped"
	reachConditional = "conditional"
)

// effectivePermissionsTx lists the caller's grants for each environment as
// module → action → reach, read from the same effective grants and scope
// rules as the V3 policy. The caller has already checked environment.view
// for every environment, which the policy requires before any module grant.
func effectivePermissionsTx(ctx context.Context, tx pgx.Tx, userID string, environments []string) (map[string]map[string]map[string]string, error) {
	out := map[string]map[string]map[string]string{}
	for _, environmentID := range environments {
		rows, err := tx.Query(ctx, `SELECT g.module,g.action,
 bool_or(g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=$2))
 FROM v3_effective_role_grants g
 WHERE g.user_id=$1
 AND (g.action<>'approve' OR (g.v3_identity_kind='human' AND g.role_kind<>'machine' AND g.assurance='passkey'))
 AND ((g.scope_kind='global' AND g.scope_id='*')
   OR (g.scope_kind='environment' AND g.scope_id=$2)
   OR (g.scope_kind='application' AND EXISTS (SELECT 1 FROM v3_application_environments ae
        WHERE ae.application_id=g.scope_id AND ae.environment_id=$2))
   OR g.scope_kind='team')
 GROUP BY g.module,g.action
 ORDER BY g.module,g.action`, userID, environmentID)
		if err != nil {
			return nil, err
		}
		modules := map[string]map[string]string{}
		for rows.Next() {
			var module, action string
			var wide bool
			if err = rows.Scan(&module, &action, &wide); err != nil {
				break
			}
			reach := reachScoped
			switch {
			case action == "approve":
				reach = reachConditional
			case wide:
				reach = reachAll
			}
			if modules[module] == nil {
				modules[module] = map[string]string{}
			}
			modules[module][action] = reach
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		out[environmentID] = modules
	}
	return out, nil
}
