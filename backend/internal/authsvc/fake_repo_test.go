package authsvc

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
)

// fakeRepo 基于内存 map 的 Repository 实现，用于 service 层单测
// （不依赖真实 PostgreSQL，保证测试快速可重复）。
type fakeRepo struct {
	mu        sync.Mutex
	users     map[string]*User         // key: username
	usersByID map[string]*User         // key: userID(string)
	tokens    map[string]*RefreshToken // key: tokenHash
	agents    map[string]*Agent        // key: agentID
	nextUser  int64
	nextToken int64
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		users:     make(map[string]*User),
		usersByID: make(map[string]*User),
		tokens:    make(map[string]*RefreshToken),
		agents:    make(map[string]*Agent),
	}
}

func (f *fakeRepo) CreateUser(_ context.Context, username, passwordHash string, role Role, tags []Tag) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[username]; ok {
		return nil, apperr.New(apperr.CodeAlreadyExists, "用户名已被注册")
	}
	f.nextUser++
	u := &User{
		ID:           strconv.FormatInt(f.nextUser, 10),
		Username:     username,
		PasswordHash: passwordHash,
		Role:         role,
		Status:       1,
		Tags:         append([]Tag(nil), tags...),
		CreatedAt:    time.Now(),
	}
	f.users[username] = u
	f.usersByID[u.ID] = u
	// 返回克隆而非存储对象本身：调用方（如 AdminCreateUser 清零 PasswordHash
	// 防泄露）只应影响响应，不得污染存储中的凭据哈希。
	return cloneUser(u), nil
}

// AddUserTag 追加/覆盖标签（fake：内存操作）。
func (f *fakeRepo) AddUserTag(_ context.Context, id string, tag Tag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.usersByID[id]
	if !ok {
		return apperr.New(apperr.CodeNotFound, "用户不存在")
	}
	u.Tags = upsertTag(u.Tags, tag)
	return nil
}

// ListUsers 分页查询用户（keyword 模糊匹配用户名；scope 非空 = 仅含该智能体标签；含标签）。
func (f *fakeRepo) ListUsers(_ context.Context, keyword, scope string, page, pageSize int) ([]*User, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	matched := make([]*User, 0, len(f.users))
	for _, u := range f.users {
		if keyword != "" && !strings.Contains(u.Username, keyword) {
			continue
		}
		if scope != "" && !u.HasTag(tagKeyAgent, scope) {
			continue
		}
		matched = append(matched, cloneUser(u))
	}
	// 按 ID 倒序（与真实 SQL 一致）
	sort.Slice(matched, func(i, j int) bool {
		a, _ := strconv.ParseInt(matched[i].ID, 10, 64)
		b, _ := strconv.ParseInt(matched[j].ID, 10, 64)
		return a > b
	})
	total := len(matched)
	lo := (page - 1) * pageSize
	if lo > total {
		lo = total
	}
	hi := lo + pageSize
	if hi > total {
		hi = total
	}
	return matched[lo:hi], total, nil
}

// ListUsersByIDs 按 ID 批量查询用户（scope 非空 = 仅本智能体组；顺序与请求一致）。
func (f *fakeRepo) ListUsersByIDs(_ context.Context, ids []int64, scope string) ([]*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*User, 0, len(ids))
	for _, id := range ids {
		u, ok := f.usersByID[strconv.FormatInt(id, 10)]
		if !ok {
			continue
		}
		if scope != "" && !u.HasTag(tagKeyAgent, scope) {
			continue
		}
		out = append(out, cloneUser(u))
	}
	return out, nil
}

// UpdateUserRole 更新用户角色（阶段3）。
func (f *fakeRepo) UpdateUserRole(_ context.Context, userID int64, role Role) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strconv.FormatInt(userID, 10)
	u, ok := f.usersByID[id]
	if !ok {
		return apperr.New(apperr.CodeNotFound, "用户不存在")
	}
	u.Role = role
	return nil
}

// UpdateUserPassword 重置用户密码（fake：直接覆盖哈希）。
func (f *fakeRepo) UpdateUserPassword(_ context.Context, userID int64, passwordHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strconv.FormatInt(userID, 10)
	u, ok := f.usersByID[id]
	if !ok {
		return apperr.New(apperr.CodeNotFound, "用户不存在")
	}
	u.PasswordHash = passwordHash
	return nil
}

// DeleteUser 删除用户（fake：从两个索引同时移除）。
func (f *fakeRepo) DeleteUser(_ context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strconv.FormatInt(userID, 10)
	u, ok := f.usersByID[id]
	if !ok {
		return apperr.New(apperr.CodeNotFound, "用户不存在")
	}
	delete(f.usersByID, id)
	delete(f.users, u.Username)
	return nil
}

// CountRole 统计指定角色用户数。
func (f *fakeRepo) CountRole(_ context.Context, role Role) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, u := range f.usersByID {
		if u.Role == role {
			n++
		}
	}
	return n, nil
}

// GetFirstSuperAdmin 返回 ID 最小的最高超管。
func (f *fakeRepo) GetFirstSuperAdmin(_ context.Context) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var first *User
	for _, u := range f.usersByID {
		if u.Role != RoleSuperAdmin {
			continue
		}
		a, _ := strconv.ParseInt(u.ID, 10, 64)
		b := int64(0)
		if first != nil {
			b, _ = strconv.ParseInt(first.ID, 10, 64)
		}
		if first == nil || a < b {
			first = u
		}
	}
	if first == nil {
		return nil, apperr.New(apperr.CodeNotFound, "用户不存在")
	}
	return cloneUser(first), nil
}

// CreateAgent 创建智能体（fake：内存操作）。
func (f *fakeRepo) CreateAgent(_ context.Context, a *Agent) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.agents[a.ID]; ok {
		return nil, apperr.New(apperr.CodeAlreadyExists, "智能体 ID 已存在")
	}
	now := time.Now()
	cp := *a
	cp.Status = 1
	cp.CreatedAt = now
	cp.UpdatedAt = now
	f.agents[a.ID] = &cp
	return &cp, nil
}

func (f *fakeRepo) GetAgent(_ context.Context, id string) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, apperr.New(apperr.CodeNotFound, "智能体不存在")
}

// UpdateAgent 更新智能体元数据（fake：全量覆盖，空串=清空，与 SQL 语义一致）。
func (f *fakeRepo) UpdateAgent(_ context.Context, a *Agent) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.agents[a.ID]
	if !ok {
		return nil, apperr.New(apperr.CodeNotFound, "智能体不存在")
	}
	cur.Name = a.Name
	cur.Description = a.Description
	cur.Model = a.Model
	cur.Avatar = a.Avatar
	cur.Welcome = a.Welcome
	cur.SystemPrompt = a.SystemPrompt
	cur.ReasoningEffort = a.ReasoningEffort
	cur.UpdatedAt = time.Now()
	cp := *cur
	return &cp, nil
}

// SetAgentStatus 启停智能体（fake：status 直接覆盖）。
func (f *fakeRepo) SetAgentStatus(_ context.Context, id string, status int) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.agents[id]
	if !ok {
		return nil, apperr.New(apperr.CodeNotFound, "智能体不存在")
	}
	cur.Status = status
	cur.UpdatedAt = time.Now()
	cp := *cur
	return &cp, nil
}

// DeleteAgent 软删除智能体（fake：置 status=0 并保留记录）。
func (f *fakeRepo) DeleteAgent(_ context.Context, id string) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.agents[id]
	if !ok {
		return nil, apperr.New(apperr.CodeNotFound, "智能体不存在")
	}
	cur.Status = 0
	cur.UpdatedAt = time.Now()
	cp := *cur
	return &cp, nil
}

func (f *fakeRepo) ListAgents(_ context.Context) ([]*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Agent, 0, len(f.agents))
	for _, a := range f.agents {
		cp := *a
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeRepo) EnsureDefaultAgent(_ context.Context, ownerUserID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.agents["tutor"]; ok {
		return nil
	}
	now := time.Now()
	f.agents["tutor"] = &Agent{
		ID: "tutor", Name: "智能助手", OwnerUserID: ownerUserID,
		Status: 1, CreatedAt: now, UpdatedAt: now,
	}
	return nil
}

func (f *fakeRepo) GetUserByUsername(_ context.Context, username string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[username]; ok {
		return cloneUser(u), nil
	}
	return nil, apperr.New(apperr.CodeNotFound, "用户不存在")
}

func (f *fakeRepo) GetUserByID(_ context.Context, id string) (*User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.usersByID[id]; ok {
		return cloneUser(u), nil
	}
	return nil, apperr.New(apperr.CodeNotFound, "用户不存在")
}

func (f *fakeRepo) CreateRefreshToken(_ context.Context, userID int64, familyID, tokenHash string, expiresAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextToken++
	f.tokens[tokenHash] = &RefreshToken{
		ID:        f.nextToken,
		UserID:    userID,
		FamilyID:  familyID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	}
	return nil
}

func (f *fakeRepo) GetRefreshTokenByHash(_ context.Context, tokenHash string) (*RefreshToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tokens[tokenHash]; ok {
		cp := *t // 拷贝，避免测试修改污染存储
		return &cp, nil
	}
	return nil, apperr.New(apperr.CodeUnauthenticated, "refresh token 无效")
}

func (f *fakeRepo) RevokeRefreshToken(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tokens {
		if t.ID == id {
			now := time.Now()
			t.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeRepo) RevokeFamily(_ context.Context, familyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, t := range f.tokens {
		if t.FamilyID == familyID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}

// RevokeFamilyByUser 吊销指定用户全部有效令牌。
func (f *fakeRepo) RevokeFamilyByUser(_ context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, t := range f.tokens {
		if t.UserID == userID && t.RevokedAt == nil {
			t.RevokedAt = &now
		}
	}
	return nil
}

// setTokenExpired 测试辅助：把某 token 的过期时间改为过去（模拟过期）。
func (f *fakeRepo) setTokenExpired(tokenHash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tokens[tokenHash]; ok {
		t.ExpiresAt = time.Now().Add(-time.Minute)
	}
}

// cloneUser 深拷贝用户，避免测试断言时被并发修改。
func cloneUser(u *User) *User {
	cp := *u
	cp.Tags = append([]Tag(nil), u.Tags...)
	return &cp
}
