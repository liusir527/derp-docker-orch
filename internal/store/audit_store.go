package store

import (
	"context"
	"fmt"
	"time"
)

// AddAudit 写入审计日志。
func (s *Store) AddAudit(ctx context.Context, a AuditLog) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_logs (kind, instance_id, code, username, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.Kind, a.InstanceID, a.Code, a.Username, a.Detail, a.CreatedAt.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("写入审计日志: %w", err)
	}
	return nil
}

// ListAudit 审计日志,时间倒序,limit 分页。
func (s *Store) ListAudit(ctx context.Context, limit int) ([]*AuditLog, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, instance_id, code, username, detail, created_at
		 FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("列出审计日志: %w", err)
	}
	defer rows.Close()
	var out []*AuditLog
	for rows.Next() {
		var (
			a         AuditLog
			createdAt string
		)
		if err := rows.Scan(&a.ID, &a.Kind, &a.InstanceID, &a.Code, &a.Username, &a.Detail, &createdAt); err != nil {
			return nil, err
		}
		if a.CreatedAt, err = time.Parse(timeFmt, createdAt); err != nil {
			return nil, fmt.Errorf("解析审计时间 %q: %w", createdAt, err)
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
