package store

import (
	"database/sql"
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
