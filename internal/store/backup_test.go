package store

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Felix2yu/guanfu/internal/model"
)

// sqliteMagic 是 SQLite 数据库文件头 16 字节，外部备份工具按它校验产物。
const sqliteMagic = "SQLite format 3\x00"

func TestWriteSQLiteSnapshotHasMagic(t *testing.T) {
	st := openTemp(t)
	_ = mustUpsert(t, st, &model.Event{
		Source: "huozhi", ExternalID: "snap-1", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1000, LocalDate: "2026-10-14",
		Title: "快照行", AmountCents: 100, Currency: "CNY",
	})

	var buf bytes.Buffer
	if err := st.WriteSQLiteSnapshot(&buf); err != nil {
		t.Fatalf("WriteSQLiteSnapshot 失败: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte(sqliteMagic)) {
		t.Fatalf("快照前 16 字节不是 SQLite 魔数: %q", buf.Bytes()[:16])
	}
}

func TestSnapshotReopensWithRows(t *testing.T) {
	st := openTemp(t)
	_ = mustUpsert(t, st, &model.Event{
		Source: "huozhi", ExternalID: "r-1", EventType: model.EventTransaction,
		TimePrecision: model.PrecisionSecond, OccurredAt: 1000, LocalDate: "2026-10-14",
		Title: "午饭", AmountCents: 2350, Currency: "CNY",
	})
	_ = mustUpsert(t, st, &model.Event{
		Source: "mujian", ExternalID: "r-2", EventType: model.EventPerformance,
		TimePrecision: model.PrecisionDay, OccurredAt: 2000, LocalDate: "2026-10-15",
		Title: "音乐会",
	})

	var buf bytes.Buffer
	if err := st.WriteSQLiteSnapshot(&buf); err != nil {
		t.Fatalf("WriteSQLiteSnapshot 失败: %v", err)
	}
	snapPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(snapPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("落盘快照失败: %v", err)
	}

	rep, err := OpenForExport(snapPath)
	if err != nil {
		t.Fatalf("重开快照失败: %v", err)
	}
	defer rep.Close()
	list, err := rep.ListEvents(EventQuery{})
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("快照行数异常: 期望 2，实际 %d", len(list))
	}
	if list[0].Title != "午饭" || list[0].AmountCents != 2350 {
		t.Fatalf("快照内容异常: %+v", list[0])
	}
}

func TestOpenForExportNeverCreatesDB(t *testing.T) {
	// 库文件不存在必须报错，且一个字节都不落——导出进程不能凭空造空库。
	path := filepath.Join(t.TempDir(), "sub", "absent.db")
	if _, err := OpenForExport(path); err == nil {
		t.Fatal("库文件不存在时 OpenForExport 应报错")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("导出路径创建了数据库文件: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("导出路径创建了数据目录")
	}
}

func TestOpenForExportSkipsMigrations(t *testing.T) {
	// 手工建一个只有 legacy 表的库（不经 Open，所以没有 events/sources）。
	// 导出链路（OpenForExport + WriteSQLiteSnapshot）跑完后，源库与快照的
	// schema 都必须原样——迁移只属于 Open。
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("打开裸库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE legacy (x TEXT)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	st, err := OpenForExport(path)
	if err != nil {
		t.Fatalf("OpenForExport 失败: %v", err)
	}
	var buf bytes.Buffer
	if err := st.WriteSQLiteSnapshot(&buf); err != nil {
		t.Fatalf("WriteSQLiteSnapshot 失败: %v", err)
	}
	st.Close()

	if got := tableNames(t, path); len(got) != 1 || got[0] != "legacy" {
		t.Fatalf("导出过程改动了源库 schema: %v", got)
	}
	snapPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(snapPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("落盘快照失败: %v", err)
	}
	if got := tableNames(t, snapPath); len(got) != 1 || got[0] != "legacy" {
		t.Fatalf("快照里混入了迁移产物: %v", got)
	}
}

// tableNames 列出库里全部表名（含 sqlite 内部表已按 type 过滤）。
func tableNames(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开 %s 失败: %v", path, err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("查询 %s schema 失败: %v", path, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		names = append(names, n)
	}
	return names
}
