package authsvc

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Steve5201/agent-backend/internal/errors"
	authpb "github.com/Steve5201/agent-backend/internal/proto/auth/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// metadataUserID 客户端身份透传键：gateway 解析 JWT 后经 gRPC metadata 注入
// 的用户 ID（键小写）。服务端只信 metadata，不信请求体。
const metadataUserID = "x-user-id"

// grpcServer AuthService 的 gRPC 传输层实现。
// 业务错误统一返回 *errors.Error（见 internal/errors），其 GRPCStatus()
// 由 gRPC 框架自动序列化为标准 status，无需手动转换。
type grpcServer struct {
	authpb.UnimplementedAuthServiceServer
	svc *Service
}

// RegisterAuthService 将 authsvc 业务服务注册为 AuthService gRPC 服务。
func RegisterAuthService(srv grpc.ServiceRegistrar, svc *Service) {
	authpb.RegisterAuthServiceServer(srv, &grpcServer{svc: svc})
}

// Register 注册新用户（P2-20）。agent_id 来自 gateway 解析的路径参数。
func (g *grpcServer) Register(ctx context.Context, req *authpb.RegisterRequest) (*authpb.RegisterResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	u, err := g.svc.Register(ctx, req.Username, req.Password, req.AgentId)
	if err != nil {
		return nil, err
	}
	return &authpb.RegisterResponse{UserId: u.ID, Username: u.Username}, nil
}

// Login 登录（P2-21）。agent_id 非空且用户尚无该标签时补写。
func (g *grpcServer) Login(ctx context.Context, req *authpb.LoginRequest) (*authpb.LoginResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	res, err := g.svc.Login(ctx, req.Username, req.Password, req.AgentId)
	if err != nil {
		return nil, err
	}
	return &authpb.LoginResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresIn:    res.ExpiresIn,
		User:         toProtoUser(res.User),
	}, nil
}

// Refresh 刷新令牌（P2-22）。
func (g *grpcServer) Refresh(ctx context.Context, req *authpb.RefreshRequest) (*authpb.RefreshResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	res, err := g.svc.Refresh(ctx, req.RefreshToken)
	if err != nil {
		return nil, err
	}
	return &authpb.RefreshResponse{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		ExpiresIn:    res.ExpiresIn,
	}, nil
}

// Logout 登出（P2-23）。
func (g *grpcServer) Logout(ctx context.Context, req *authpb.LogoutRequest) (*authpb.LogoutResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	if err := g.svc.Logout(ctx, req.RefreshToken); err != nil {
		return nil, err
	}
	return &authpb.LogoutResponse{}, nil
}

// Me 返回当前用户资料（P2-24）。user_id 从 gRPC metadata 的 x-user-id 读取。
func (g *grpcServer) Me(ctx context.Context, _ *authpb.MeRequest) (*authpb.MeResponse, error) {
	userID := userIDFromMetadata(ctx)
	if userID == "" {
		return nil, unauthenticated("缺少用户身份")
	}
	u, err := g.svc.Me(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &authpb.MeResponse{User: toProtoUser(u)}, nil
}

// ChangePassword 用户自助修改密码（user_id 从 metadata 读取，不信任请求体）。
func (g *grpcServer) ChangePassword(ctx context.Context, req *authpb.ChangePasswordRequest) (*authpb.ChangePasswordResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	userID := userIDFromMetadata(ctx)
	if userID == "" {
		return nil, unauthenticated("缺少用户身份")
	}
	if err := g.svc.ChangePassword(ctx, userID, req.OldPassword, req.NewPassword); err != nil {
		return nil, err
	}
	return &authpb.ChangePasswordResponse{}, nil
}

// AdminCreateUser 管理员创建用户（调用者身份从 metadata 读取，service 内做分层校验）。
func (g *grpcServer) AdminCreateUser(ctx context.Context, req *authpb.AdminCreateUserRequest) (*authpb.AdminCreateUserResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	u, err := g.svc.AdminCreateUser(ctx, actorID, req.Username, req.Password, req.Role, req.AgentId, toTags(req.Tags))
	if err != nil {
		return nil, err
	}
	return &authpb.AdminCreateUserResponse{User: toProtoUser(u)}, nil
}

// AdminListUsers 管理员分页查询用户（调用者身份从 metadata 读取，按管辖范围过滤）。
func (g *grpcServer) AdminListUsers(ctx context.Context, req *authpb.AdminListUsersRequest) (*authpb.AdminListUsersResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	users, total, err := g.svc.AdminListUsers(ctx, actorID, req.Keyword, int(req.Page), int(req.PageSize))
	if err != nil {
		return nil, err
	}
	out := make([]*authpb.User, 0, len(users))
	for _, u := range users {
		out = append(out, toProtoUser(u))
	}
	return &authpb.AdminListUsersResponse{Users: out, Total: int32(total)}, nil
}

// AdminUpdateUser 管理员重置用户密码（调用者身份从 metadata 读取，
// 角色层级校验在 service 内完成）。
func (g *grpcServer) AdminUpdateUser(ctx context.Context, req *authpb.AdminUpdateUserRequest) (*authpb.AdminUpdateUserResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	u, err := g.svc.AdminUpdateUser(ctx, actorID, req.UserId, req.NewPassword)
	if err != nil {
		return nil, err
	}
	return &authpb.AdminUpdateUserResponse{User: toProtoUser(u)}, nil
}

// AdminDeleteUser 管理员删除用户（调用者身份从 metadata 读取）。
func (g *grpcServer) AdminDeleteUser(ctx context.Context, req *authpb.AdminDeleteUserRequest) (*authpb.AdminDeleteUserResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	if err := g.svc.AdminDeleteUser(ctx, actorID, req.UserId); err != nil {
		return nil, err
	}
	return &authpb.AdminDeleteUserResponse{}, nil
}

// AdminGetUsersByIds 按 ID 批量查询用户（数据管理模块 Top 用户用户名回填）。
// 调用者身份从 metadata 读取，service 内做角色/管辖范围分层校验。
func (g *grpcServer) AdminGetUsersByIds(ctx context.Context, req *authpb.AdminGetUsersByIdsRequest) (*authpb.AdminGetUsersByIdsResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	ids := make([]int64, 0, len(req.UserIds))
	for _, id := range req.UserIds {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			return nil, invalidArgument("非法的用户 ID")
		}
		ids = append(ids, n)
	}
	users, err := g.svc.AdminGetUsersByIds(ctx, actorID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*authpb.User, 0, len(users))
	for _, u := range users {
		out = append(out, toProtoUser(u))
	}
	return &authpb.AdminGetUsersByIdsResponse{Users: out}, nil
}

// ListAgents 智能体列表（按调用者角色返回可见范围）。
func (g *grpcServer) ListAgents(ctx context.Context, _ *authpb.ListAgentsRequest) (*authpb.ListAgentsResponse, error) {
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	agents, err := g.svc.ListAgents(ctx, actorID)
	if err != nil {
		return nil, err
	}
	out := make([]*authpb.Agent, 0, len(agents))
	for _, a := range agents {
		out = append(out, toProtoAgent(a))
	}
	return &authpb.ListAgentsResponse{Agents: out}, nil
}

// CreateAgent 创建智能体（仅最高超管；owner_user_id 可选，空 = 稍后绑定）。
func (g *grpcServer) CreateAgent(ctx context.Context, req *authpb.CreateAgentRequest) (*authpb.CreateAgentResponse, error) {
	if req == nil {
		return nil, invalidArgument("请求体不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	var ownerID int64
	var err error
	if req.OwnerUserId != "" {
		ownerID, err = parseInt64(req.OwnerUserId)
		if err != nil {
			return nil, invalidArgument("owner_user_id 必须为数字")
		}
	}
	a, err := g.svc.CreateAgent(ctx, actorID, req.Id, req.Name, req.Description, req.Model,
		req.Avatar, req.Welcome, req.SystemPrompt, req.ReasoningEffort, ownerID)
	if err != nil {
		return nil, err
	}
	return &authpb.CreateAgentResponse{Agent: toProtoAgent(a)}, nil
}

// BindAgentOwner 绑定/更换/解绑智能体超管（仅最高超管；owner_user_id 空 = 解绑）。
func (g *grpcServer) BindAgentOwner(ctx context.Context, req *authpb.BindAgentOwnerRequest) (*authpb.BindAgentOwnerResponse, error) {
	if req == nil || req.Id == "" {
		return nil, invalidArgument("id 不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	var ownerID int64
	var err error
	if req.OwnerUserId != "" {
		ownerID, err = parseInt64(req.OwnerUserId)
		if err != nil {
			return nil, invalidArgument("owner_user_id 必须为数字")
		}
	}
	a, err := g.svc.BindAgentOwner(ctx, actorID, req.Id, ownerID)
	if err != nil {
		return nil, err
	}
	return &authpb.BindAgentOwnerResponse{Agent: toProtoAgent(a)}, nil
}

// GetAgent 智能体详情（super_admin 任意；agent_admin 限自身归属域）。
func (g *grpcServer) GetAgent(ctx context.Context, req *authpb.GetAgentRequest) (*authpb.GetAgentResponse, error) {
	if req == nil || req.Id == "" {
		return nil, invalidArgument("id 不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	a, err := g.svc.GetAgent(ctx, actorID, req.Id)
	if err != nil {
		return nil, err
	}
	return &authpb.GetAgentResponse{Agent: toProtoAgent(a)}, nil
}

// GetAgentPublic 公开智能体元数据（任意登录用户；白名单字段）。
func (g *grpcServer) GetAgentPublic(ctx context.Context, req *authpb.GetAgentRequest) (*authpb.GetAgentPublicResponse, error) {
	if req == nil || req.Id == "" {
		return nil, invalidArgument("id 不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	a, err := g.svc.GetAgentPublic(ctx, actorID, req.Id)
	if err != nil {
		return nil, err
	}
	return &authpb.GetAgentPublicResponse{
		Id:              a.ID,
		Name:            a.Name,
		Description:     a.Description,
		Avatar:          a.Avatar,
		Welcome:         a.Welcome,
		SystemPrompt:    a.SystemPrompt,
		ReasoningEffort: a.ReasoningEffort,
		Status:          int32(a.Status),
	}, nil
}

// UpdateAgent 更新智能体元数据（super_admin 任意；agent_admin 限自身归属域）。
func (g *grpcServer) UpdateAgent(ctx context.Context, req *authpb.UpdateAgentRequest) (*authpb.UpdateAgentResponse, error) {
	if req == nil || req.Id == "" {
		return nil, invalidArgument("id 不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	a, err := g.svc.UpdateAgent(ctx, actorID, Agent{
		ID:              req.Id,
		Name:            req.Name,
		Description:     req.Description,
		Model:           req.Model,
		Avatar:          req.Avatar,
		Welcome:         req.Welcome,
		SystemPrompt:    req.SystemPrompt,
		ReasoningEffort: req.ReasoningEffort,
	})
	if err != nil {
		return nil, err
	}
	return &authpb.UpdateAgentResponse{Agent: toProtoAgent(a)}, nil
}

// SetAgentStatus 启停智能体（仅最高超管）。
func (g *grpcServer) SetAgentStatus(ctx context.Context, req *authpb.SetAgentStatusRequest) (*authpb.SetAgentStatusResponse, error) {
	if req == nil || req.Id == "" {
		return nil, invalidArgument("id 不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	a, err := g.svc.SetAgentStatus(ctx, actorID, req.Id, int(req.Status))
	if err != nil {
		return nil, err
	}
	return &authpb.SetAgentStatusResponse{Agent: toProtoAgent(a)}, nil
}

// DeleteAgent 软删除智能体（仅最高超管）。
func (g *grpcServer) DeleteAgent(ctx context.Context, req *authpb.DeleteAgentRequest) (*authpb.DeleteAgentResponse, error) {
	if req == nil || req.Id == "" {
		return nil, invalidArgument("id 不能为空")
	}
	actorID := userIDFromMetadata(ctx)
	if actorID == "" {
		return nil, unauthenticated("缺少调用者身份")
	}
	if err := g.svc.DeleteAgent(ctx, actorID, req.Id); err != nil {
		return nil, err
	}
	return &authpb.DeleteAgentResponse{}, nil
}

// userIDFromMetadata 从入站 metadata 读取 x-user-id。
func userIDFromMetadata(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(metadataUserID); len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// toProtoUser 领域模型 → proto（绝不携带 password_hash）。
func toProtoUser(u *User) *authpb.User {
	out := &authpb.User{
		Id:        u.ID,
		Username:  u.Username,
		Role:      string(u.Role),
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
	if len(u.Tags) > 0 {
		out.Tags = make([]*authpb.Tag, 0, len(u.Tags))
		for _, t := range u.Tags {
			out.Tags = append(out.Tags, &authpb.Tag{Key: t.Key, Value: t.Value})
		}
	}
	return out
}

// toTags proto Tag 列表 → 领域模型（nil 安全）。
func toTags(pb []*authpb.Tag) []Tag {
	if len(pb) == 0 {
		return nil
	}
	out := make([]Tag, 0, len(pb))
	for _, t := range pb {
		if t == nil {
			continue
		}
		out = append(out, Tag{Key: t.Key, Value: t.Value})
	}
	return out
}

// toProtoAgent 领域模型 → proto。
func toProtoAgent(a *Agent) *authpb.Agent {
	return &authpb.Agent{
		Id:              a.ID,
		Name:            a.Name,
		Description:     a.Description,
		Model:           a.Model,
		OwnerUserId:     fmt.Sprint(a.OwnerUserID),
		Status:          int32(a.Status),
		CreatedAt:       timestamppb.New(a.CreatedAt),
		UpdatedAt:       timestamppb.New(a.UpdatedAt),
		Avatar:          a.Avatar,
		Welcome:         a.Welcome,
		SystemPrompt:    a.SystemPrompt,
		ReasoningEffort: a.ReasoningEffort,
	}
}

// parseInt64 解析字符串 int64（用于 proto 中的 string 型 ID 转领域模型）。
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

// invalidArgument 构造参数错误（传输层便捷函数）。
func invalidArgument(msg string) error {
	return errors.New(errors.CodeInvalidArgument, msg)
}

// unauthenticated 构造认证错误（传输层便捷函数）。
func unauthenticated(msg string) error {
	return errors.New(errors.CodeUnauthenticated, msg)
}
