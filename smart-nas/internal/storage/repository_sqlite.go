// repository_sqlite.go 元数据访问层（SQLite 实现，v0.11）。
//
// 此前元数据以单个 TOML 文件全量落盘（每写一条重写整个文件），浏览大目录
// 时 I/O 与序列化代价随记录数平方级增长。现改为 SQLite（modernc.org/sqlite
// 纯 Go 驱动，免 cgo），按行增量写入，parent/md5/删除时间建索引；
// 公共 API 与旧实现保持一致，服务层无感知。
//
// 旧版 file_metas.toml / file_versions.toml（及更早的 .json）在库为空时
// 自动导入，导入成功后原文件改名为 *.migrated 保留备份。
package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"

	"smart-nas/pkg/logger"
)

// Repository 元数据访问层（SQLite）
type Repository struct {
	db  *sql.DB
	path string
}

// schema 建表语句：时间以 Unix 纳秒整数存储，NULL 表示零值（未删除/未设置）
const schema = `
CREATE TABLE IF NOT EXISTS file_metas (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	name           TEXT    NOT NULL DEFAULT '',
	parent_id      INTEGER NOT NULL DEFAULT 0,
	is_dir         INTEGER NOT NULL DEFAULT 0,
	size           INTEGER NOT NULL DEFAULT 0,
	md5            TEXT    NOT NULL DEFAULT '',
	mime_type      TEXT    NOT NULL DEFAULT '',
	storage_path   TEXT    NOT NULL DEFAULT '',
	trash_origin   TEXT    NOT NULL DEFAULT '',
	owner_id       INTEGER NOT NULL DEFAULT 0,
	shared         INTEGER NOT NULL DEFAULT 0,
	share_token    TEXT    NOT NULL DEFAULT '',
	share_expires  INTEGER,
	share_pwd_hash TEXT    NOT NULL DEFAULT '',
	deleted_at     INTEGER,
	created_at     INTEGER NOT NULL DEFAULT 0,
	updated_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_fm_parent  ON file_metas(parent_id);
CREATE INDEX IF NOT EXISTS idx_fm_md5     ON file_metas(md5);
CREATE INDEX IF NOT EXISTS idx_fm_deleted ON file_metas(deleted_at);
CREATE INDEX IF NOT EXISTS idx_fm_token   ON file_metas(share_token);

CREATE TABLE IF NOT EXISTS file_versions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	file_id      INTEGER NOT NULL DEFAULT 0,
	version      INTEGER NOT NULL DEFAULT 0,
	md5          TEXT    NOT NULL DEFAULT '',
	storage_path TEXT    NOT NULL DEFAULT '',
	size         INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_fv_file ON file_versions(file_id);
`

// NewRepository 打开（或创建）元数据库并完成旧数据迁移。
// dbPath 为 SQLite 文件路径；legacyDir 为旧 TOML/JSON 元数据所在目录。
func NewRepository(dbPath string, legacyDir string) (*Repository, error) {
	if dbPath == "" {
		return nil, errors.New("元数据库路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	// WAL + NORMAL：增量写入性能；busy_timeout 缓解读写竞争
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite 并发写依赖连接池串行化；单连接避免 database is locked
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("初始化元数据库失败: %w", err)
	}
	r := &Repository{db: db, path: dbPath}
	if err := r.migrateLegacy(legacyDir); err != nil {
		_ = db.Close()
		return nil, err
	}
	return r, nil
}

// DBPath 返回元数据库文件路径（供列表过滤自身）
func (r *Repository) DBPath() string { return r.path }

// Close 关闭数据库（WAL 模式下安全落盘）
func (r *Repository) Close() error { return r.db.Close() }

// ---- 时间与行映射 ----

func nanos(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixNano()
}

func timeOf(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(0, v.Int64).Local()
}

const fileCols = `id, name, parent_id, is_dir, size, md5, mime_type, storage_path,
	trash_origin, owner_id, shared, share_token, share_expires, share_pwd_hash,
	deleted_at, created_at, updated_at`

func scanFile(row interface{ Scan(...any) error }) (*FileMeta, error) {
	var f FileMeta
	var shareExpires, deletedAt, createdAt, updatedAt sql.NullInt64
	err := row.Scan(&f.ID, &f.Name, &f.ParentID, &f.IsDir, &f.Size, &f.MD5, &f.MimeType,
		&f.StoragePath, &f.TrashOrigin, &f.OwnerID, &f.Shared, &f.ShareToken,
		&shareExpires, &f.SharePwdHash, &deletedAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	// 时间以 Unix 纳秒整数存储，需显式转换（database/sql 不会自动 int64→time.Time）
	f.ShareExpires = timeOf(shareExpires)
	f.DeletedAt = timeOf(deletedAt)
	f.CreatedAt = timeOf(createdAt)
	f.UpdatedAt = timeOf(updatedAt)
	return &f, nil
}

func fileValues(f *FileMeta) []any {
	return []any{f.Name, f.ParentID, f.IsDir, f.Size, f.MD5, f.MimeType, f.StoragePath,
		f.TrashOrigin, f.OwnerID, f.Shared, f.ShareToken, nanos(f.ShareExpires),
		f.SharePwdHash, nanos(f.DeletedAt), nanos(f.CreatedAt), nanos(f.UpdatedAt)}
}

// ---- 文件元数据 ----

// Create 创建元数据（ID 由 SQLite 自增分配）
func (r *Repository) Create(f *FileMeta) error {
	f.CreatedAt = time.Now()
	f.UpdatedAt = f.CreatedAt
	res, err := r.db.Exec(`INSERT INTO file_metas (`+strings.TrimPrefix(fileCols, "id, ")+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fileValues(f)...)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	f.ID = uint(id)
	return nil
}

// Update 更新元数据（保持 created_at 与 ID 不变）
func (r *Repository) Update(f *FileMeta) error {
	f.UpdatedAt = time.Now()
	// created_at 取调用方记录中的原值（Get 加载后未变），fileValues 已按纳序列输出
	vals := fileValues(f)
	res, err := r.db.Exec(`UPDATE file_metas SET
		name=?, parent_id=?, is_dir=?, size=?, md5=?, mime_type=?, storage_path=?,
		trash_origin=?, owner_id=?, shared=?, share_token=?, share_expires=?,
		share_pwd_hash=?, deleted_at=?, created_at=?, updated_at=?
		WHERE id=?`, append(vals, f.ID)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("文件不存在")
	}
	return nil
}

// Get 按 ID 查询
func (r *Repository) Get(id uint) (*FileMeta, error) {
	row := r.db.QueryRow(`SELECT `+fileCols+` FROM file_metas WHERE id=?`, id)
	f, err := scanFile(row)
	if err != nil {
		return nil, errors.New("文件不存在")
	}
	return f, nil
}

// All 全部元数据（含回收站）。磁盘根同步、统计、分享解析等扫描场景使用
func (r *Repository) All() map[string]*FileMeta {
	out := make(map[string]*FileMeta)
	rows, err := r.db.Query(`SELECT ` + fileCols + ` FROM file_metas`)
	if err != nil {
		logger.Error("读取元数据失败", "error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			continue
		}
		out[strconv.FormatUint(uint64(f.ID), 10)] = f
	}
	return out
}

// ByParent 某目录下未被删除的子项（共享文件系统，不按用户隔离）
func (r *Repository) ByParent(parentID uint) []*FileMeta {
	return r.queryList(`SELECT `+fileCols+` FROM file_metas
		WHERE parent_id=? AND deleted_at IS NULL ORDER BY is_dir DESC, name`, parentID)
}

// ByParentName 按父目录 + 名称查找未删除的子项（走 parent 索引）
func (r *Repository) ByParentName(parentID uint, name string) *FileMeta {
	row := r.db.QueryRow(`SELECT `+fileCols+` FROM file_metas
		WHERE parent_id=? AND name=? AND deleted_at IS NULL LIMIT 1`, parentID, name)
	f, err := scanFile(row)
	if err != nil {
		return nil
	}
	return f
}

// ByMD5 查找同 MD5 同大小的未删除文件（秒传去重，走 md5 索引）
func (r *Repository) ByMD5(md5 string, size int64) *FileMeta {
	if md5 == "" {
		return nil
	}
	row := r.db.QueryRow(`SELECT `+fileCols+` FROM file_metas
		WHERE md5=? AND size=? AND is_dir=0 AND deleted_at IS NULL LIMIT 1`, md5, size)
	f, err := scanFile(row)
	if err != nil {
		return nil
	}
	return f
}

// Search 按关键词模糊搜索（文件名）
func (r *Repository) Search(keyword string) []*FileMeta {
	return r.queryList(`SELECT `+fileCols+` FROM file_metas
		WHERE deleted_at IS NULL AND instr(name, ?) > 0 ORDER BY is_dir DESC, name`, keyword)
}

// InTrash 回收站列表
func (r *Repository) InTrash() []*FileMeta {
	return r.queryList(`SELECT ` + fileCols + ` FROM file_metas WHERE deleted_at IS NOT NULL`)
}

// Delete 删除记录（软删除标记）
func (r *Repository) Delete(f *FileMeta) error {
	f.DeletedAt = time.Now()
	return r.Update(f)
}

// Purge 彻底删除记录
func (r *Repository) Purge(id uint) error {
	_, err := r.db.Exec(`DELETE FROM file_metas WHERE id=?`, id)
	return err
}

func (r *Repository) queryList(q string, args ...any) []*FileMeta {
	var out []*FileMeta
	rows, err := r.db.Query(q, args...)
	if err != nil {
		logger.Error("查询元数据失败", "error", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			continue
		}
		out = append(out, f)
	}
	return out
}

// ---- 版本 ----

// AddVersion 记录历史版本（ID 自增）
func (r *Repository) AddVersion(v *FileVersion) error {
	v.CreatedAt = time.Now()
	res, err := r.db.Exec(`INSERT INTO file_versions (file_id, version, md5, storage_path, size, created_at)
		VALUES (?,?,?,?,?,?)`, v.FileID, v.Version, v.MD5, v.StoragePath, v.Size, nanos(v.CreatedAt))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	v.ID = uint(id)
	return nil
}

// Versions 指定文件的版本列表（新→旧）
func (r *Repository) Versions(fileID uint) []*FileVersion {
	var out []*FileVersion
	rows, err := r.db.Query(`SELECT id, file_id, version, md5, storage_path, size, created_at
		FROM file_versions WHERE file_id=? ORDER BY created_at DESC, id DESC`, fileID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v FileVersion
		var created sql.NullInt64
		if err := rows.Scan(&v.ID, &v.FileID, &v.Version, &v.MD5, &v.StoragePath, &v.Size, &created); err != nil {
			continue
		}
		v.CreatedAt = timeOf(created)
		out = append(out, &v)
	}
	return out
}

// TrimVersions 保留最近 keep 个版本，返回被移除的版本（用于物理删除）
func (r *Repository) TrimVersions(fileID uint, keep int) []*FileVersion {
	vs := r.Versions(fileID)
	if len(vs) <= keep {
		return nil
	}
	var removed []*FileVersion
	for _, v := range vs[keep:] {
		if _, err := r.db.Exec(`DELETE FROM file_versions WHERE id=?`, v.ID); err == nil {
			removed = append(removed, v)
		}
	}
	return removed
}

// ---- 旧数据迁移 ----

// migrateLegacy 库为空时导入旧版 TOML/JSON 元数据，成功后原文件改名 *.migrated
func (r *Repository) migrateLegacy(legacyDir string) error {
	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM file_metas`).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return nil
	}
	if legacyDir == "" {
		return nil
	}
	n, err := r.importLegacyFile(filepath.Join(legacyDir, "file_metas.toml"), "file_metas.json", true)
	if err != nil {
		return err
	}
	vn, err := r.importLegacyFile(filepath.Join(legacyDir, "file_versions.toml"), "file_versions.json", false)
	if err != nil {
		return err
	}
	if n+vn > 0 {
		logger.Info("旧版元数据已迁移至 SQLite", "files", n, "versions", vn, "db", r.path)
	}
	return nil
}

// importLegacyFile 读取旧文件（优先 TOML，其次 JSON）并整批导入
func (r *Repository) importLegacyFile(tomlPath, jsonName string, isMeta bool) (int, error) {
	data, used, err := readLegacy(tomlPath, filepath.Join(filepath.Dir(tomlPath), jsonName))
	if err != nil || used == "" {
		return 0, err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return 0, err
	}
	n := 0
	if isMeta {
		var old map[string]*FileMeta
		if err := unmarshalLegacy(data, used, &old); err != nil {
			return 0, err
		}
		stmt, err := tx.Prepare(`INSERT INTO file_metas (id, name, parent_id, is_dir, size, md5,
			mime_type, storage_path, trash_origin, owner_id, shared, share_token, share_expires,
			share_pwd_hash, deleted_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return 0, err
		}
		for _, f := range old {
			if _, err := stmt.Exec(f.ID, f.Name, f.ParentID, f.IsDir, f.Size, f.MD5, f.MimeType,
				f.StoragePath, f.TrashOrigin, f.OwnerID, f.Shared, f.ShareToken,
				nanos(f.ShareExpires), f.SharePwdHash, nanos(f.DeletedAt),
				nanos(f.CreatedAt), nanos(f.UpdatedAt)); err != nil {
				_ = tx.Rollback()
				return 0, err
			}
			n++
		}
	} else {
		var old map[string]*FileVersion
		if err := unmarshalLegacy(data, used, &old); err != nil {
			return 0, err
		}
		stmt, err := tx.Prepare(`INSERT INTO file_versions (id, file_id, version, md5, storage_path, size, created_at)
			VALUES (?,?,?,?,?,?,?)`)
		if err != nil {
			return 0, err
		}
		for _, v := range old {
			if _, err := stmt.Exec(v.ID, v.FileID, v.Version, v.MD5, v.StoragePath, v.Size, nanos(v.CreatedAt)); err != nil {
				_ = tx.Rollback()
				return 0, err
			}
			n++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	_ = os.Rename(used, used+".migrated")
	return n, nil
}

// readLegacy 依优先级读取旧文件：TOML → JSON；都不存在返回空路径
func readLegacy(tomlPath, jsonPath string) ([]byte, string, error) {
	if data, err := os.ReadFile(tomlPath); err == nil {
		return data, tomlPath, nil
	}
	if data, err := os.ReadFile(jsonPath); err == nil {
		return data, jsonPath, nil
	}
	return nil, "", nil
}

func unmarshalLegacy(data []byte, path string, out any) error {
	if strings.HasSuffix(path, ".json") {
		return json.Unmarshal(data, out)
	}
	return toml.Unmarshal(data, out)
}

// VersionBytes 历史版本占用字节数（含孤儿版本）
func (r *Repository) VersionBytes() int64 {
	var total sql.NullInt64
	_ = r.db.QueryRow(`SELECT SUM(size) FROM file_versions`).Scan(&total)
	if !total.Valid {
		return 0
	}
	return total.Int64
}
