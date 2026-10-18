package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Felix2yu/guanfu/internal/model"
	"github.com/Felix2yu/guanfu/internal/store"
)

// TestRunBackup 覆盖 `guanfu backup` 子命令——外部备份工具（docker-db-auto-backup）
// 的入口契约：产物以 SQLite 魔数开头，stdout 只有快照字节，日志走 stderr。
func TestRunBackupWritesSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "guanfu.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if _, err := st.UpsertEvent(&model.Event{
		Source: "huozhi", ExternalID: "b-1", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1000, LocalDate: "2026-10-14",
		Title: "备份行",
	}); err != nil {
		t.Fatalf("UpsertEvent 失败: %v", err)
	}
	st.Close()

	var buf bytes.Buffer
	if err := runBackup(dbPath, &buf); err != nil {
		t.Fatalf("runBackup 失败: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("SQLite format 3\x00")) {
		t.Fatal("产物应以 SQLite 魔数开头")
	}
}

func TestRunBackupMissingDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "absent.db")
	var buf bytes.Buffer
	if err := runBackup(dbPath, &buf); err == nil {
		t.Fatal("库文件不存在时 runBackup 应报错")
	}
	if buf.Len() != 0 {
		t.Fatal("失败时 stdout 不应有任何字节")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("backup 不得创建数据库文件")
	}
}
