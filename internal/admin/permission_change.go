package admin

import (
	"context"
	"database/sql"

	"wedding-project/internal/authorization"
)

// permissionSnapshot records effective access at every scope affected by a
// user's assignment or override change. A global deny can affect all venues.
func permissionSnapshot(ctx context.Context, tx *sql.Tx, userID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.key, s.venue_id FROM permissions p CROSS JOIN (
  SELECT NULL::text AS venue_id UNION
  SELECT venue_id FROM user_roles WHERE user_id=$1 AND venue_id IS NOT NULL UNION
  SELECT venue_id FROM user_permission_overrides WHERE user_id=$1 AND venue_id IS NOT NULL
 ) s`, userID)
	if err != nil {
		return nil, err
	}
	type item struct {
		key   string
		venue sql.NullString
	}
	var items []item
	for rows.Next() {
		var current item
		if err = rows.Scan(&current.key, &current.venue); err != nil {
			break
		}
		items = append(items, current)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	snapshot := make(map[string]bool, len(items))
	for _, current := range items {
		var venue *string
		scope := "g"
		if current.venue.Valid {
			venue = &current.venue.String
			scope = "v" + current.venue.String
		}
		allowed, err := authorization.HasPermissionTx(ctx, tx, userID, current.key, venue)
		if err != nil {
			return nil, err
		}
		snapshot[scope+"\x00"+current.key] = allowed
	}
	return snapshot, nil
}

// rejectNewPermissions checks the completed transaction, including trigger
// cleanup, before allowing a removal to commit. Role removal must only revoke
// access; any new permission needs a separate, explicit grant operation.
func rejectNewPermissions(ctx context.Context, tx *sql.Tx, target string, before map[string]bool) error {
	for scopedKey, previouslyAllowed := range before {
		if previouslyAllowed {
			continue
		}
		separator := -1
		for index := range scopedKey {
			if scopedKey[index] == 0 {
				separator = index
				break
			}
		}
		scope, key := scopedKey[:separator], scopedKey[separator+1:]
		var venue *string
		if scope != "g" {
			venueID := scope[1:]
			venue = &venueID
		}
		allowed, err := authorization.HasPermissionTx(ctx, tx, target, key, venue)
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		return errForbidden
	}
	return nil
}
