// Package admin implements operator bootstrap and administrative management.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
)

// BootstrapVerifier checks the configured identity using a trusted WorkOS API.
type BootstrapVerifier interface {
	Verify(context.Context, string, string, string) error
}

// WorkOSVerifier verifies a user and the role on an active organization membership.
type WorkOSVerifier struct{ Client *workos.Client }

// Verify requires the exact verified user, organization and membership role.
func (verifier WorkOSVerifier) Verify(ctx context.Context, userID, organizationID, roleSlug string) error {
	user, err := verifier.Client.UserManagement().Get(ctx, userID)
	if err != nil || user == nil || user.ID != userID || !user.EmailVerified {
		return errors.New("WorkOS identity verification failed")
	}
	memberships := verifier.Client.UserManagement().ListOrganizationMemberships(ctx, &workos.UserManagementListOrganizationMembershipsParams{
		UserID: &userID, OrganizationID: &organizationID,
	})
	found := false
	for memberships.Next() {
		membership := memberships.Current()
		if membership.UserID == userID && membership.OrganizationID == organizationID && string(membership.Status) == "active" && membership.Role != nil && membership.Role.Slug == roleSlug {
			found = true
		}
	}
	if memberships.Err() != nil || !found {
		return errors.New("WorkOS bootstrap membership verification failed")
	}
	return nil
}

// Bootstrap activates a previously authenticated local user exactly once.
func Bootstrap(ctx context.Context, database *sql.DB, verifier BootstrapVerifier, userID, organizationID, roleSlug string) error {
	if database == nil || verifier == nil || userID == "" || organizationID == "" || roleSlug == "" {
		return errors.New("missing bootstrap configuration")
	}
	if err := verifier.Verify(ctx, userID, organizationID, roleSlug); err != nil {
		return err
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("bootstrap unavailable")
	}
	defer tx.Rollback()
	// Serialize bootstrap attempts and future privileged mutations.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(87239651)`); err != nil {
		return errors.New("bootstrap unavailable")
	}
	var localID string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE workos_id=$1 FOR UPDATE`, userID).Scan(&localID); err != nil {
		return errors.New("matching local user not found")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO admin_bootstrap (singleton,user_id) VALUES (TRUE,$1)`, localID); err != nil {
		return errors.New("bootstrap already completed or unavailable")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET is_active=TRUE, updated_at=now() WHERE id=$1`, localID); err != nil {
		return errors.New("bootstrap unavailable")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO user_roles (id,user_id,role_id) SELECT $1,$2,id FROM roles WHERE name='Admin' AND scope='global'`, uuid.NewString(), localID)
	if err != nil {
		return errors.New("bootstrap unavailable")
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("global Admin role missing")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (id,actor_user_id,action,target_type,target_id,metadata) VALUES ($1,$2,'admin.bootstrap','user',$2::text,jsonb_build_object('outcome','success'))`, uuid.NewString(), localID); err != nil {
		return errors.New("bootstrap audit unavailable")
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("bootstrap commit failed")
	}
	return nil
}
