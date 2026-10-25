package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRegionNotFound 地区不存在。
var ErrRegionNotFound = errors.New("地区不存在")

// ErrRegionInUse 地区仍有实例,禁止删除。
var ErrRegionInUse = errors.New("地区下仍有实例")

const regionCols = `id, name, base_domain, region_code, agent_url, agent_token, max_containers, enabled, created_at`

// EnsureRegion 幂等插入地区(启动 seed 用)。已存在则不修改。
func (s *Store) EnsureRegion(ctx context.Context, r *Region) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO regions (`+regionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		r.ID, r.Name, r.BaseDomain, r.RegionCode, r.AgentURL, r.AgentToken,
		r.MaxContainers, boolInt(r.Enabled), r.CreatedAt.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("初始化地区: %w", err)
	}
	return nil
}

// CreateRegion 新建地区。ID 冲突返回错误。
func (s *Store) CreateRegion(ctx context.Context, r *Region) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO regions (`+regionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.BaseDomain, r.RegionCode, r.AgentURL, r.AgentToken,
		r.MaxContainers, boolInt(r.Enabled), r.CreatedAt.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("创建地区: %w", err)
	}
	return nil
}

// UpdateRegion 更新可变字段(不改 ID/创建时间)。
func (s *Store) UpdateRegion(ctx context.Context, r *Region) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE regions SET name = ?, base_domain = ?, region_code = ?, agent_url = ?,
		 agent_token = ?, max_containers = ?, enabled = ? WHERE id = ?`,
		r.Name, r.BaseDomain, r.RegionCode, r.AgentURL, r.AgentToken,
		r.MaxContainers, boolInt(r.Enabled), r.ID)
	if err != nil {
		return fmt.Errorf("更新地区: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRegionNotFound
	}
	return nil
}

// GetRegion 按 slug 查询。
func (s *Store) GetRegion(ctx context.Context, id string) (*Region, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+regionCols+` FROM regions WHERE id = ?`, id)
	r, err := scanRegion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRegionNotFound
	}
	return r, err
}

// ListRegions 全部地区,按创建时间升序(本地地区通常最前)。
func (s *Store) ListRegions(ctx context.Context) ([]*Region, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+regionCols+` FROM regions ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("列出地区: %w", err)
	}
	defer rows.Close()
	var out []*Region
	for rows.Next() {
		r, err := scanRegion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRegion 删除地区。仍有实例时拒绝,避免实例指向不存在的地区。
func (s *Store) DeleteRegion(ctx context.Context, id string) error {
	n, err := s.CountInstancesByRegion(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrRegionInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM regions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除地区: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrRegionNotFound
	}
	return nil
}

// CountInstancesByRegion 地区下实例数(含已过期未清理的)。
func (s *Store) CountInstancesByRegion(ctx context.Context, id string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances WHERE region = ?`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计地区实例: %w", err)
	}
	return n, nil
}

// BackfillInstanceRegion 把历史空归属实例写入本地地区 slug。
func (s *Store) BackfillInstanceRegion(ctx context.Context, slug string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE instances SET region = ? WHERE region = ''`, slug)
	if err != nil {
		return 0, fmt.Errorf("回填实例地区: %w", err)
	}
	return res.RowsAffected()
}

func scanRegion(row scanner) (*Region, error) {
	var (
		r         Region
		enabled   int
		createdAt string
	)
	if err := row.Scan(&r.ID, &r.Name, &r.BaseDomain, &r.RegionCode, &r.AgentURL,
		&r.AgentToken, &r.MaxContainers, &enabled, &createdAt); err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	t, err := time.Parse(timeFmt, createdAt)
	if err != nil {
		return nil, fmt.Errorf("解析地区 created_at %q: %w", createdAt, err)
	}
	r.CreatedAt = t
	return &r, nil
}
