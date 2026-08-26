package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrCodeUnavailable 兑换码不可用(无效/禁用/已用/被占用)。
var ErrCodeUnavailable = errors.New("兑换码无效或不可用")

// ErrCodeNotFound 兑换码不存在。
var ErrCodeNotFound = errors.New("兑换码不存在")

// reservationTTL 预留有效期:抢占后 15 分钟内未消费自动释放。
const reservationTTL = 15 * time.Minute

const redeemCodeCols = `id, code, duration_value, duration_unit, disabled,
	used_by_instance_id, used_at, created_by, created_at, note`

// CreateRedeemCodes 批量插入兑换码。返回实际成功列表;单个冲突跳过。
func (s *Store) CreateRedeemCodes(ctx context.Context, codes []RedeemCode) ([]RedeemCode, error) {
	var created []RedeemCode
	for i := range codes {
		c := codes[i]
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO redeem_codes (code, duration_value, duration_unit, disabled, used_by_instance_id, used_at, created_by, created_at, note)
			 VALUES (?, ?, ?, ?, NULL, NULL, ?, ?, ?)`,
			c.Code, c.DurationValue, c.DurationUnit, boolInt(c.Disabled), c.CreatedBy,
			c.CreatedAt.UTC().Format(timeFmt), c.Note)
		if err != nil {
			if isUniqueViolation(err) {
				continue // 同码并发冲突,跳过
			}
			return nil, fmt.Errorf("插入兑换码: %w", err)
		}
		created = append(created, c)
	}
	return created, nil
}

// GetRedeemCode 按码查询。
func (s *Store) GetRedeemCode(ctx context.Context, code string) (*RedeemCode, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+redeemCodeCols+` FROM redeem_codes WHERE code = ?`, code)
	return scanRedeemCode(row)
}

// ListRedeemCodes 列兑换码。status: all|unused|used;limit 分页(0 表示不限制)。
// 返回按创建时间倒序。
func (s *Store) ListRedeemCodes(ctx context.Context, status string, limit, offset int) ([]*RedeemCode, int, error) {
	where := ""
	switch status {
	case "unused":
		where = "WHERE used_at IS NULL"
	case "used":
		where = "WHERE used_at IS NOT NULL"
	}
	query := `SELECT ` + redeemCodeCols + ` FROM redeem_codes ` + where + ` ORDER BY created_at DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("列出兑换码: %w", err)
	}
	defer rows.Close()
	var out []*RedeemCode
	for rows.Next() {
		c, err := scanRedeemCode(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redeem_codes `+where).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ReserveRedeemCode 原子抢占兑换码(15 分钟 TTL)。成功返回预留 token。
// 并发安全:BEGIN IMMEDIATE + 条件 UPDATE(仅未用且未被有效预留的码可抢)。
func (s *Store) ReserveRedeemCode(ctx context.Context, code string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	staleBefore := time.Now().UTC().Add(-reservationTTL).Format(timeFmt)
	now := time.Now().UTC().Format(timeFmt)
	res, err := tx.ExecContext(ctx,
		`UPDATE redeem_codes
		 SET reserved_token = ?, reserved_at = ?
		 WHERE code = ?
		   AND disabled = 0
		   AND used_at IS NULL
		   AND (reserved_token IS NULL OR reserved_at < ?)`,
		token, now, code, staleBefore)
	if err != nil {
		return "", fmt.Errorf("预留兑换码: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrCodeUnavailable
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// ReleaseRedeemCode 释放预留(消费失败或流程中断时调用)。
func (s *Store) ReleaseRedeemCode(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE redeem_codes SET reserved_token = NULL, reserved_at = NULL
		 WHERE reserved_token = ? AND used_at IS NULL`, token)
	return err
}

// ConsumeRedeemCode 消费预留的兑换码,绑定到实例。原子条件更新。
func (s *Store) ConsumeRedeemCode(ctx context.Context, token, instanceID string) error {
	staleBefore := time.Now().UTC().Add(-reservationTTL).Format(timeFmt)
	now := time.Now().UTC().Format(timeFmt)
	res, err := s.db.ExecContext(ctx,
		`UPDATE redeem_codes
		 SET used_by_instance_id = ?, used_at = ?, reserved_token = NULL, reserved_at = NULL
		 WHERE reserved_token = ?
		   AND used_at IS NULL
		   AND disabled = 0
		   AND reserved_at >= ?`,
		instanceID, now, token, staleBefore)
	if err != nil {
		return fmt.Errorf("消费兑换码: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrCodeUnavailable
	}
	return nil
}

// GetRedeemCodeByToken 按预留 token 查询(供消费前校验时长)。
func (s *Store) GetRedeemCodeByToken(ctx context.Context, token string) (*RedeemCode, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+redeemCodeCols+` FROM redeem_codes WHERE reserved_token = ?`, token)
	return scanRedeemCode(row)
}

// DisableRedeemCode 停用兑换码(幂等)。
func (s *Store) DisableRedeemCode(ctx context.Context, code string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE redeem_codes SET disabled = 1 WHERE code = ? AND used_at IS NULL`, code)
	return err
}

// CountRedeemCodes 统计:总数、未用、已用。
func (s *Store) CountRedeemCodes(ctx context.Context) (total, unused, used int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redeem_codes`).Scan(&total); err != nil {
		return
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redeem_codes WHERE used_at IS NULL`).Scan(&unused); err != nil {
		return
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redeem_codes WHERE used_at IS NOT NULL`).Scan(&used); err != nil {
		return
	}
	return
}

func scanRedeemCode(row scanner) (*RedeemCode, error) {
	var (
		c         RedeemCode
		usedBy    sql.NullString
		usedAt    sql.NullString
		createdAt string
	)
	if err := row.Scan(&c.ID, &c.Code, &c.DurationValue, &c.DurationUnit, &c.Disabled,
		&usedBy, &usedAt, &c.CreatedBy, &createdAt, &c.Note); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCodeNotFound
		}
		return nil, err
	}
	if usedBy.Valid {
		c.UsedByInstanceID = &usedBy.String
	}
	if usedAt.Valid {
		t, err := time.Parse(timeFmt, usedAt.String)
		if err != nil {
			return nil, fmt.Errorf("解析 used_at %q: %w", usedAt.String, err)
		}
		c.UsedAt = &t
	}
	var err error
	if c.CreatedAt, err = time.Parse(timeFmt, createdAt); err != nil {
		return nil, fmt.Errorf("解析 created_at %q: %w", createdAt, err)
	}
	return &c, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
