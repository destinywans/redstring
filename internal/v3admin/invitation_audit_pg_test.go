package v3admin

import (
	"context"
	"errors"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5/pgconn"
)

type invitationAuditSender struct{}

func (invitationAuditSender) SendInvitation(context.Context, InvitationMessage) error { return nil }

func invitationAuditFixture(t *testing.T) (*Service, core.User, string) {
	t.Helper()
	// The existing fixture creates and drops its own random schema, performs
	// the complete migrations and bootstraps an actual admin session.
	s, actor, token := reminderLockFixture(t)
	var err error
	s.Invitations, err = NewInvitationService(invitationAuditSender{}, "https://testlab.invalid/accept-invitation")
	if err != nil {
		t.Fatal(err)
	}
	return s, actor, token
}

func invitationAuditState(t *testing.T, s *Service, id string) (string, int, int) {
	t.Helper()
	var state string
	var relay, accepted int
	err := s.Store.DB.QueryRow(t.Context(), `SELECT status,
 (SELECT count(*) FROM management_audit WHERE action='v3.member.invitation.relay_accepted' AND target=i.id),
 (SELECT count(*) FROM management_audit WHERE action='v3.member.invitation.accepted' AND changes->>'invitation_id'=i.id)
 FROM v3_member_invitations i WHERE id=$1`, id).Scan(&state, &relay, &accepted)
	if err != nil {
		t.Fatal(err)
	}
	return state, relay, accepted
}

func requireInvitationAuditRejection(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || pgErr.Message != "synthetic invitation audit rejection" {
		t.Fatalf("expected actual audit trigger rejection, got %v", err)
	}
}

func TestInvitationDeliveryAuditCommitsOncePostgres(t *testing.T) {
	s, actor, token := invitationAuditFixture(t)
	id, code, _, err := s.prepareInvitation(t.Context(), actor, token, "dev", invitationInput{Email: "invite-delivery@testlab.invalid", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.confirmInvitationDelivery(t.Context(), actor, token, "dev", id); err != nil {
		t.Fatalf("relay acceptance transaction failed: %v", err)
	}
	if state, relay, accepted := invitationAuditState(t, s, id); state != "delivered" || relay != 1 || accepted != 0 {
		t.Fatalf("relay transaction evidence: %s %d %d", state, relay, accepted)
	}
	var environment string
	if err = s.Store.DB.QueryRow(t.Context(), `SELECT changes->>'environment' FROM management_audit WHERE target=$1 AND action='v3.member.invitation.relay_accepted'`, id).Scan(&environment); err != nil || environment != "dev" {
		t.Fatalf("relay audit environment=%q err=%v", environment, err)
	}
	if err = s.confirmInvitationDelivery(t.Context(), actor, token, "dev", id); !errors.Is(err, errInvitationInvalid) {
		t.Fatalf("duplicate relay confirmation: %v", err)
	}
	input := invitationAcceptance{Code: code, Name: "Invited viewer", Password: "synthetic-invitation-password"}
	if err = s.acceptInvitation(t.Context(), input); err != nil {
		t.Fatalf("acceptance transaction failed: %v", err)
	}
	if err = s.acceptInvitation(t.Context(), input); !errors.Is(err, errInvitationInvalid) {
		t.Fatalf("duplicate acceptance: %v", err)
	}
	if state, relay, accepted := invitationAuditState(t, s, id); state != "accepted" || relay != 1 || accepted != 1 {
		t.Fatalf("exactly-once audits: %s %d %d", state, relay, accepted)
	}
	user, _, err := s.Store.Login(t.Context(), "invite-delivery@testlab.invalid", input.Password)
	if err != nil {
		t.Fatal(err)
	}
	// PA-55/D6: the new account reads every environment through the viewer's
	// one global environment grant, and holds no other environment grant.
	var grants, global int
	if err = s.Store.DB.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE action='view' AND scope_kind='global')
 FROM v3_effective_role_grants WHERE user_id=$1 AND module='environment'`, user.ID).Scan(&grants, &global); err != nil || grants != 1 || global != 1 {
		t.Fatalf("new account environment grants=%d global=%d err=%v", grants, global, err)
	}
}

// PA-55: the viewer's global environment view keeps invitations safe; an
// environment-scoped environment grant on the viewer does not, because an
// invitation would then hand out access to one chosen environment.
func TestInvitationViewerSafetyAllowsOnlyGlobalEnvironmentViewPostgres(t *testing.T) {
	s, actor, token := invitationAuditFixture(t)
	ctx := t.Context()
	safe := func() bool {
		t.Helper()
		tx, err := s.Store.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		ok, err := safeInvitationViewer(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	var global bool
	if err := s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants WHERE role_id='viewer'
 AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*')`).Scan(&global); err != nil || !global || !safe() {
		t.Fatalf("migrated viewer: global=%t safe=%t err=%v", global, safe(), err)
	}
	if detail, _, err := s.getRole(ctx, "viewer", actor, token, []string{"dev"}, false); err != nil ||
		len(detail.EnvironmentAccess) != 1 || detail.EnvironmentAccess[0] != "global" || len(detail.Matrix) != len(matrixCatalog) {
		t.Fatalf("migrated viewer detail: access=%v err=%v", detail.EnvironmentAccess, err)
	}
	if _, err := s.Store.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if safe() {
		t.Fatal("viewer with an environment-scoped environment grant is still safe to invite")
	}
	if _, _, _, err := s.prepareInvitation(ctx, actor, token, "dev", invitationInput{Email: "invite-unsafe@testlab.invalid", Role: "viewer"}); err == nil {
		t.Fatal("invitation accepted while the viewer is unsafe")
	}
}

func TestInvitationAcceptanceAuditCommitsPostgres(t *testing.T) {
	s, actor, token := invitationAuditFixture(t)
	id, code, _, err := s.prepareInvitation(t.Context(), actor, token, "dev", invitationInput{Email: "invite-accept@testlab.invalid", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	// Isolate acceptance from the independently tested confirmation path so
	// its own invalid SQL cannot be hidden behind the earlier failure.
	if _, err = s.Store.DB.Exec(t.Context(), `UPDATE v3_member_invitations SET status='delivered',delivered_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = s.acceptInvitation(t.Context(), invitationAcceptance{Code: code, Name: "Accepted viewer", Password: "synthetic-invitation-password"}); err != nil {
		t.Fatalf("acceptance transaction failed: %v", err)
	}
	if state, relay, accepted := invitationAuditState(t, s, id); state != "accepted" || relay != 0 || accepted != 1 {
		t.Fatalf("acceptance audit evidence: %s %d %d", state, relay, accepted)
	}
}

func TestInvitationAuditFailureRollsBackPostgres(t *testing.T) {
	s, actor, token := invitationAuditFixture(t)
	id, code, _, err := s.prepareInvitation(t.Context(), actor, token, "dev", invitationInput{Email: "invite-rollback@testlab.invalid", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	// An actual PostgreSQL trigger injects an audit INSERT failure. Every
	// preceding change must roll back, while the original code remains valid
	// for a later successfully committed acceptance.
	if _, err = s.Store.DB.Exec(t.Context(), `CREATE FUNCTION reject_invitation_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'synthetic invitation audit rejection' USING ERRCODE='P0001'; END $$;
 CREATE TRIGGER reject_invitation_audit BEFORE INSERT ON management_audit FOR EACH ROW
 WHEN (NEW.action IN ('v3.member.invitation.relay_accepted','v3.member.invitation.accepted'))
 EXECUTE FUNCTION reject_invitation_audit()`); err != nil {
		t.Fatal(err)
	}
	err = s.confirmInvitationDelivery(t.Context(), actor, token, "dev", id)
	requireInvitationAuditRejection(t, err)
	if state, relay, accepted := invitationAuditState(t, s, id); state != "pending" || relay != 0 || accepted != 0 {
		t.Fatalf("relay audit rollback failed: %s %d %d", state, relay, accepted)
	}
	input := invitationAcceptance{Code: code, Name: "Rollback viewer", Password: "synthetic-invitation-password"}
	if err = s.acceptInvitation(t.Context(), input); !errors.Is(err, errInvitationInvalid) {
		t.Fatalf("unconfirmed relay code must be unusable: %v", err)
	}
	if _, err = s.Store.DB.Exec(t.Context(), `ALTER TABLE management_audit DISABLE TRIGGER reject_invitation_audit`); err != nil {
		t.Fatal(err)
	}
	if err = s.confirmInvitationDelivery(t.Context(), actor, token, "dev", id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(t.Context(), `ALTER TABLE management_audit ENABLE TRIGGER reject_invitation_audit`); err != nil {
		t.Fatal(err)
	}
	err = s.acceptInvitation(t.Context(), input)
	requireInvitationAuditRejection(t, err)
	var accounts, settings int
	if err = s.Store.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM users WHERE email='invite-rollback@testlab.invalid'),
 (SELECT count(*) FROM v3_user_settings s JOIN users u ON u.id=s.user_id WHERE u.email='invite-rollback@testlab.invalid')`).Scan(&accounts, &settings); err != nil || accounts != 0 || settings != 0 {
		t.Fatalf("partial account persisted: accounts=%d settings=%d err=%v", accounts, settings, err)
	}
	if state, relay, accepted := invitationAuditState(t, s, id); state != "delivered" || relay != 1 || accepted != 0 {
		t.Fatalf("acceptance audit rollback failed: %s %d %d", state, relay, accepted)
	}
	if _, err = s.Store.DB.Exec(t.Context(), `ALTER TABLE management_audit DISABLE TRIGGER reject_invitation_audit`); err != nil {
		t.Fatal(err)
	}
	if err = s.acceptInvitation(t.Context(), input); err != nil {
		t.Fatalf("original code cannot recover after rolled-back audit: %v", err)
	}
	if state, relay, accepted := invitationAuditState(t, s, id); state != "accepted" || relay != 1 || accepted != 1 {
		t.Fatalf("recovery audit evidence: %s %d %d", state, relay, accepted)
	}
}
