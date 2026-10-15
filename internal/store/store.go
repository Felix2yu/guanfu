// Package store 负责 SQLite（modernc 纯 Go 驱动）连接、schema 迁移
// 与 events / sources 两张核心表的读写。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // 纯 Go SQLite，无 CGO，与全家桶范式一致
)

// Store 数据访问入口。
type Store struct {
	DB *sql.DB
}

// Open 打开（必要时创建）SQLite 库并执行迁移。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}
	// WAL 模式 + busy_timeout：读并发不互斥，单文件部署下足够。
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(4) // SQLite 单写者，控制并发写连接数
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			source        TEXT    NOT NULL,
			external_id   TEXT    NOT NULL,
			event_type    TEXT    NOT NULL,
			time_precision TEXT   NOT NULL DEFAULT 'second',
			occurred_at   INTEGER NOT NULL,
			local_date    TEXT    NOT NULL,
			title         TEXT    NOT NULL DEFAULT '',
			summary       TEXT    NOT NULL DEFAULT '',
			amount_cents  INTEGER NOT NULL DEFAULT 0,
			currency      TEXT    NOT NULL DEFAULT '',
			latitude      REAL    NOT NULL DEFAULT 0,
			longitude     REAL    NOT NULL DEFAULT 0,
			location_name TEXT    NOT NULL DEFAULT '',
			media_json    TEXT    NOT NULL DEFAULT '',
			detail_json   TEXT    NOT NULL DEFAULT '',
			source_url    TEXT    NOT NULL DEFAULT '',
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_events_source_external
			ON events(source, external_id)`,
		`CREATE INDEX IF NOT EXISTS idx_events_local_date
			ON events(local_date)`,
		`CREATE INDEX IF NOT EXISTS idx_events_occurred_at
			ON events(occurred_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type
			ON events(event_type)`,
		`CREATE TABLE IF NOT EXISTS sources (
			name           TEXT PRIMARY KEY,
			enabled        INTEGER NOT NULL DEFAULT 0,
			interval_secs  INTEGER NOT NULL DEFAULT 0,
			cursor         TEXT    NOT NULL DEFAULT '',
			last_sync_at   INTEGER NOT NULL DEFAULT 0,
			last_error     TEXT    NOT NULL DEFAULT '',
			last_count     INTEGER NOT NULL DEFAULT 0,
			updated_at     INTEGER NOT NULL DEFAULT 0
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.DB.Exec(stmt); err != nil {
			return fmt.Errorf("迁移失败: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}
