package store

import (
	"context"
	"time"
)

// AuditEntry is an append-only record of a significant action. There is
// deliberately no delete/update path in this file: the audit log must
// survive "Recent retrievals" clearing and any other user-facing deletion,
// so ClearJobs/DeleteJob log to here (recording what was removed) before
// they touch the jobs table, never the other way around.
type AuditEntry struct {
	ID            int64
	At            time.Time
	Actor, Action string
	EntityType    string
	EntityID      int64
	Details       string
}

func (s *Store) LogAudit(ctx context.Context, actor, action, entityType string, entityID int64, details string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO audit_log(actor,action,entity_type,entity_id,details) VALUES(?,?,?,?,?)`, actor, action, entityType, entityID, details)
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,at,actor,action,entity_type,entity_id,details FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.EntityType, &e.EntityID, &e.Details); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
