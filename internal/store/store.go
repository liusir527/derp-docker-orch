package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"

	_ "modernc.org/sqlite"
)

// migrations 顺序迁移版本。PRAGMA user_version 记录当前版本。
var migrations = []string{
	// v1: 实例表。所有唯一约束是并发分配的最后防线。
	`CREATE TABLE IF NOT EXISTS instances (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL UNIQUE,
		subdomain      TEXT NOT NULL UNIQUE,
		region_id      INTEGER NOT NULL UNIQUE,
		region_code    TEXT NOT NULL,
		stun_port      INTEGER NOT NULL UNIQUE,
		container_name TEXT NOT NULL UNIQUE,
		expires_at     TEXT NOT NULL,
		state          TEXT NOT NULL DEFAULT 'active',
		created_at     TEXT NOT NULL
	)`,
	// v2: 新增 host_ip 字段(nginx 直连用)。
	`ALTER TABLE instances ADD COLUMN host_ip TEXT NOT NULL DEFAULT ''`,
	// v3: 兑换码表 + 审计日志表。
	`CREATE TABLE IF NOT EXISTS redeem_codes (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		code                TEXT NOT NULL UNIQUE,
		duration_value      INTEGER NOT NULL,
		duration_unit       TEXT NOT NULL CHECK(duration_unit IN ('day','month','year')),
		disabled            INTEGER NOT NULL DEFAULT 0,
		used_by_instance_id TEXT,
		used_at             TEXT,
		created_by          TEXT NOT NULL DEFAULT 'admin',
		created_at          TEXT NOT NULL,
		reserved_token      TEXT,
		reserved_at         TEXT,
		note                TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_redeem_codes_created_at ON redeem_codes(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_redeem_codes_used_at ON redeem_codes(used_at)`,
	`CREATE TABLE IF NOT EXISTS audit_logs (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		kind        TEXT NOT NULL,
		instance_id TEXT,
		code        TEXT,
		username    TEXT,
		detail      TEXT,
		created_at  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at)`,
	// v4: 管理员密码哈希(改密码功能)。
	`CREATE TABLE IF NOT EXISTS admin_password (
		id            INTEGER PRIMARY KEY CHECK(id = 1),
		password_hash TEXT NOT NULL
	)`,
	// v5: 多地区支持。地区注册表 + 实例归属列。
	`CREATE TABLE IF NOT EXISTS regions (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		base_domain    TEXT NOT NULL,
		region_code    TEXT NOT NULL,
		agent_url      TEXT NOT NULL DEFAULT '',
		agent_token    TEXT NOT NULL DEFAULT '',
		max_containers INTEGER NOT NULL DEFAULT 0,
		enabled        INTEGER NOT NULL DEFAULT 1,
		created_at     TEXT NOT NULL
	)`,
	`ALTER TABLE instances ADD COLUMN region TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_instances_region ON instances(region)`,
}

// Store SQLite 存储。并发安全由 database/sql 连接池保证。
type Store struct {
	db *sql.DB
}

// Open 打开并迁移数据库。路径 ":memory:" 可用于测试。
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		dsn = "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite 单写者,避免锁竞争
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// randomToken 生成 n 字节随机 hex 令牌(预留/session 用)。
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) migrate() error {
	var ver int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&ver); err != nil {
		return fmt.Errorf("读取迁移版本: %w", err)
	}
	for i := ver; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("迁移 v%d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
