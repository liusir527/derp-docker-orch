package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ErrAdminNotFound 管理员记录不存在(或旧密码错误)。
var ErrAdminNotFound = errors.New("管理员记录不存在")

// GetAdminPasswordHash 读取管理员密码哈希。未初始化返回 ErrAdminNotFound。
func (s *Store) GetAdminPasswordHash(ctx context.Context) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM admin_password WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAdminNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取管理员密码: %w", err)
	}
	return hash, nil
}

// InitAdminPassword 首次写入密码哈希(仅当记录不存在)。
func (s *Store) InitAdminPassword(ctx context.Context, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("生成密码哈希: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO admin_password (id, password_hash) VALUES (1, ?)`, string(hash))
	if err != nil {
		return fmt.Errorf("初始化管理员密码: %w", err)
	}
	return nil
}

// VerifyAdminPassword 校验密码。返回 nil 表示通过。
func (s *Store) VerifyAdminPassword(ctx context.Context, password string) bool {
	hash, err := s.GetAdminPasswordHash(ctx)
	if err != nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// UpdateAdminPassword 更新密码(校验旧密码;失败返回错误)。
func (s *Store) UpdateAdminPassword(ctx context.Context, oldPassword, newPassword string) error {
	if !s.VerifyAdminPassword(ctx, oldPassword) {
		return ErrAdminNotFound // 旧密码错误
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("生成密码哈希: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE admin_password SET password_hash = ? WHERE id = 1`, string(hash))
	if err != nil {
		return fmt.Errorf("更新密码: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrAdminNotFound
	}
	return nil
}
