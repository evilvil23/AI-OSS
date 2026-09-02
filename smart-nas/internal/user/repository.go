package user

import (
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"smart-nas/internal/store"
)

// Repository 用户数据访问层
type Repository struct {
	store *store.Store[string, *User]
	next  uint
}

// NewRepository 创建用户仓库（基于 TOML 文件持久化）
func NewRepository(dataDir string) (*Repository, error) {
	s, err := store.NewStore[string, *User](filepath.Join(dataDir, "users.toml"))
	if err != nil {
		return nil, err
	}
	r := &Repository{store: s}
	for _, u := range s.All() {
		if u.ID >= r.next {
			r.next = u.ID + 1
		}
	}
	return r, nil
}

// Create 创建用户
func (r *Repository) Create(u *User) error {
	u.ID = r.next
	r.next++
	u.CreatedAt = time.Now()
	u.UpdatedAt = u.CreatedAt
	return r.store.Set(strconv.FormatUint(uint64(u.ID), 10), u)
}

// GetByID 按 ID 查询
func (r *Repository) GetByID(id uint) (*User, error) {
	u, ok := r.store.Get(strconv.FormatUint(uint64(id), 10))
	if !ok {
		return nil, errors.New("用户不存在")
	}
	return u, nil
}

// GetByUsername 按用户名查询
func (r *Repository) GetByUsername(username string) (*User, error) {
	for _, u := range r.store.All() {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, errors.New("用户不存在")
}

// Exists 用户名是否已存在
func (r *Repository) Exists(username string) bool {
	_, err := r.GetByUsername(username)
	return err == nil
}

// List 用户列表（按 ID 排序）
func (r *Repository) List() ([]*User, error) {
	all := r.store.All()
	ids := make([]uint, 0, len(all))
	for _, u := range all {
		ids = append(ids, u.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*User, 0, len(ids))
	for _, id := range ids {
		if u := all[strconv.FormatUint(uint64(id), 10)]; u != nil {
			out = append(out, u)
		}
	}
	return out, nil
}

// Update 更新用户（保持 ID 不变）
func (r *Repository) Update(u *User) error {
	old, ok := r.store.Get(strconv.FormatUint(uint64(u.ID), 10))
	if !ok {
		return errors.New("用户不存在")
	}
	u.CreatedAt = old.CreatedAt
	u.UpdatedAt = time.Now()
	return r.store.Set(strconv.FormatUint(uint64(u.ID), 10), u)
}

// Delete 删除用户
func (r *Repository) Delete(id uint) error {
	return r.store.Delete(strconv.FormatUint(uint64(id), 10))
}

// Count 用户总数
func (r *Repository) Count() int {
	return r.store.Size()
}

// CountAdmins 管理员（admin）数量
func (r *Repository) CountAdmins() int {
	n := 0
	for _, u := range r.store.All() {
		if u.Role == RoleAdmin {
			n++
		}
	}
	return n
}