package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const timeFmt = time.RFC3339

// ErrNotFound 实例不存在。
var ErrNotFound = errors.New("实例不存在")

// CreateInstance 插入新实例。所有唯一冲突都会返回错误,由调用方重试命名。
func (s *Store) CreateInstance(ctx context.Context, i *Instance) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO instances (id, name, subdomain, region_id, region_code, stun_port, container_name, expires_at, state, created_at, host_ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.Name, i.Subdomain, i.RegionID, i.RegionCode, i.STUNPort,
		i.ContainerName, i.ExpiresAt.UTC().Format(timeFmt), i.State, i.CreatedAt.UTC().Format(timeFmt), i.HostIP)
	if err != nil {
		return fmt.Errorf("创建实例: %w", err)
	}
	return nil
}

// UpdateInstanceHostIP 更新实例的 host_ip 字段(容器创建后由 orchestrator 注入)。
func (s *Store) UpdateInstanceHostIP(ctx context.Context, id, hostIP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE instances SET host_ip = ? WHERE id = ?`, hostIP, id)
	return err
}

// GetInstance 按 ID 查询。
func (s *Store) GetInstance(ctx context.Context, id string) (*Instance, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, subdomain, region_id, region_code, stun_port, container_name, expires_at, state, created_at, host_ip
		 FROM instances WHERE id = ?`, id)
	i, err := scanInstance(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return i, err
}

// ListInstances 全部实例,按创建时间倒序。
func (s *Store) ListInstances(ctx context.Context) ([]*Instance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, subdomain, region_id, region_code, stun_port, container_name, expires_at, state, created_at, host_ip
		 FROM instances ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("列出实例: %w", err)
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		i, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ListExpired 过期实例(expires_at <= now)。
func (s *Store) ListExpired(ctx context.Context, now time.Time) ([]*Instance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, subdomain, region_id, region_code, stun_port, container_name, expires_at, state, created_at, host_ip
		 FROM instances WHERE expires_at <= ?`, now.UTC().Format(timeFmt))
	if err != nil {
		return nil, fmt.Errorf("查询过期实例: %w", err)
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		i, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteInstance 删除实例行,释放 regionID/stunPort/子域名等全部资源。
func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除实例: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanInstance(row scanner) (*Instance, error) {
	var (
		i         Instance
		expiresAt string
		createdAt string
	)
	if err := row.Scan(&i.ID, &i.Name, &i.Subdomain, &i.RegionID, &i.RegionCode,
		&i.STUNPort, &i.ContainerName, &expiresAt, &i.State, &createdAt, &i.HostIP); err != nil {
		return nil, err
	}
	var err error
	if i.ExpiresAt, err = time.Parse(timeFmt, expiresAt); err != nil {
		return nil, fmt.Errorf("解析 expires_at %q: %w", expiresAt, err)
	}
	if i.CreatedAt, err = time.Parse(timeFmt, createdAt); err != nil {
		return nil, fmt.Errorf("解析 created_at %q: %w", createdAt, err)
	}
	return &i, nil
}

// CreateInstanceAllocated 在单事务内为实例分配 regionID/stunPort 并插入行。
// 事务内扫描已占用值 → 找空档 → 真实插入;并发冲突(唯一约束)回滚重试。
// region/stun 冲突在内部处理;返回错误含列名,调用方据此判断是否换名重试。
func (s *Store) CreateInstanceAllocated(ctx context.Context, inst *Instance, regionBase, stunBase int) error {
	const retries = 5
	for attempt := 0; attempt < retries; attempt++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		usedRegion, err := usedInts(ctx, tx, "region_id")
		if err != nil {
			tx.Rollback()
			return err
		}
		usedStun, err := usedInts(ctx, tx, "stun_port")
		if err != nil {
			tx.Rollback()
			return err
		}
		inst.RegionID = firstFree(regionBase, usedRegion)
		inst.STUNPort = firstFree(stunBase, usedStun)

		_, err = tx.ExecContext(ctx,
			`INSERT INTO instances (id, name, subdomain, region_id, region_code, stun_port, container_name, expires_at, state, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			inst.ID, inst.Name, inst.Subdomain, inst.RegionID, inst.RegionCode, inst.STUNPort,
			inst.ContainerName, inst.ExpiresAt.UTC().Format(timeFmt), inst.State, inst.CreatedAt.UTC().Format(timeFmt), inst.HostIP)
		if err != nil {
			tx.Rollback()
			// 并发下 region/stun 被他人占用 → 重新扫描分配。
			if isUniqueViolation(err) && !nameColumnIn(err) {
				continue
			}
			return fmt.Errorf("创建实例: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("分配资源失败: 重试 %d 次仍冲突", retries)
}

// usedInts 读取列的全部已占用值。
func usedInts(ctx context.Context, tx *sql.Tx, col string) (map[int]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+col+` FROM instances ORDER BY `+col)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		used[v] = true
	}
	return used, rows.Err()
}

// firstFree 从 base 起找第一个未被占用的值。
func firstFree(base int, used map[int]bool) int {
	c := base
	for used[c] {
		c++
	}
	return c
}

// nameColumnIn 冲突是否来自命名相关列(需换名重试)。
func nameColumnIn(err error) bool {
	msg := err.Error()
	for _, col := range []string{"instances.id", "instances.name", "instances.subdomain", "instances.container_name"} {
		if strings.Contains(msg, col) {
			return true
		}
	}
	return false
}

// isUniqueViolation 判定 SQLite 唯一约束冲突。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
