package store

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// OpenForExport 打开数据库用于备份导出。与 Open 的区别只有一点：不建目录、不跑迁移。
//
// 导出进程由外部备份工具（docker exec）在应用运行时触发：表结构升级只应由应用
// 进程自己执行，一个「顺手打开数据库」的导出进程不该改 schema，更不该与主进程抢写。
// 数据文件不存在时直接报错——那是路径配错，导出永远不该凭空造出一个空库。
func OpenForExport(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("数据文件不可用: %w", err)
	}
	// 刻意不设 journal_mode：导出连接不回写库头；已有的 WAL 数据照常读取。
	// file: 前缀让 modernc 驱动把路径与参数分开解析（与 Open 的裸路径拼法等价）。
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1) // 导出是顺序读，一条连接足够，也少开一份 -shm 句柄。
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	return &Store{DB: db}, nil
}

// WriteSQLiteSnapshot 把数据库以 SQLite 自身的一致性快照写到 w。
//
// 用 VACUUM INTO 而不是复制文件：热拷贝正在被写入的 db/-wal/-shm 不是原子操作，
// 可能拷出损坏的副本或漏掉 WAL 里已提交的数据；VACUUM INTO 由 SQLite 在事务里
// 生成一份自洽的快照，WAL 模式下也不阻塞写入。
//
// 快照先落在临时目录再流式输出：VACUUM INTO 要求目标不存在，且这样无论
// 调用方读到一半断开，都不会在数据目录里留下半成品。
func (s *Store) WriteSQLiteSnapshot(w io.Writer) error {
	dir, err := os.MkdirTemp("", "guanfu-snapshot-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(dir)

	target := filepath.Join(dir, "snapshot.db")
	if _, err := s.DB.Exec("VACUUM INTO ?", target); err != nil {
		return fmt.Errorf("导出数据库快照失败: %w", err)
	}

	f, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("读取快照失败: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("写出快照失败: %w", err)
	}
	return nil
}
