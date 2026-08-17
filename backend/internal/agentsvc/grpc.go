// grpc.go —— agent-service gRPC 传输层（P2-46）。
//
// 职责：把 AgentService proto 契约映射到 Service 业务方法。
//   - user_id 经 gRPC metadata（x-user-id）注入，请求体不含属主字段
//     （gateway 在调用前解析 JWT 并把 user_id 写入 metadata）；
//   - 错误统一返回 apperr.Error（实现 GRPCStatus，自动映射状态码）；
//   - StreamChat 把 RunStream 的文本增量逐事件下发（打字机效果）。
package agentsvc

import (
	"context"
	"encoding/json"
	"strconv"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	agentv1 "github.com/Steve5201/agent-backend/internal/proto/agent/v1"
	"github.com/Steve5201/agent-framework/agent"
	"github.com/Steve5201/agent-framework/llm"
	"github.com/Steve5201/agent-framework/schema"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// metadataUserID user_id 在 gRPC metadata 中的注入键（小写，与 P2 约定一致）。
// 调用方（gateway）解析 JWT 后必须写入；缺失/非法一律拒绝。
const metadataUserID = "x-user-id"

// metadataUserRole 调用方角色在 gRPC metadata 中的注入键（小写，q2 配额）。
// gateway 解析 JWT 后随 x-user-id 一并写入；缺失 = 无角色（按普通用户处理）。
const metadataUserRole = "x-user-role"

// GrpcServer AgentServiceServer 实现。
type GrpcServer struct {
	agentv1.UnimplementedAgentServiceServer
	svc *Service
	log *zap.Logger
}

// NewGrpcServer 创建 gRPC 传输层。
func NewGrpcServer(svc *Service, log *zap.Logger) *GrpcServer {
	return &GrpcServer{svc: svc, log: log}
}

// RegisterAgentService 注册 AgentService 到 gRPC server（cmd 入口调用）。
func RegisterAgentService(srv *grpc.Server, gs *GrpcServer) {
	agentv1.RegisterAgentServiceServer(srv, gs)
}

// ---------------------------------------------------------------------------
// 会话 CRUD
// ---------------------------------------------------------------------------

// CreateSession 创建会话（可携带初始配置）。
func (g *GrpcServer) CreateSession(ctx context.Context, req *agentv1.CreateSessionRequest) (*agentv1.CreateSessionResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := g.svc.CreateSession(ctx, userID, req.AgentId, req.Title)
	if err != nil {
		return nil, err
	}
	// 初始配置非空：创建后应用（含校验），再回读最新会话。
	if req.Config != nil {
		if _, err := g.svc.UpdateSessionConfig(ctx, userID, sess.ID, fromProtoConfig(req.Config)); err != nil {
			return nil, err
		}
		sess, err = g.svc.GetSession(ctx, userID, sess.ID)
		if err != nil {
			return nil, err
		}
	}
	g.log.Info("grpc CreateSession",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sess.ID),
		zap.String("request_id", apperr.RequestIDFromContext(ctx)),
	)
	return &agentv1.CreateSessionResponse{Session: toProtoSession(sess)}, nil
}

// ListSessions 分页列出会话。
func (g *GrpcServer) ListSessions(ctx context.Context, req *agentv1.ListSessionsRequest) (*agentv1.ListSessionsResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	list, total, err := g.svc.ListSessions(ctx, userID, req.AgentId, int(req.Page), int(req.PageSize))
	if err != nil {
		return nil, err
	}
	out := make([]*agentv1.Session, 0, len(list))
	for _, s := range list {
		out = append(out, toProtoSession(s))
	}
	return &agentv1.ListSessionsResponse{Sessions: out, Total: total}, nil
}

// GetSession 获取会话详情（非本人返回 NOT_FOUND，防枚举）。
func (g *GrpcServer) GetSession(ctx context.Context, req *agentv1.GetSessionRequest) (*agentv1.GetSessionResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	sess, err := g.svc.GetSession(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	return &agentv1.GetSessionResponse{Session: toProtoSession(sess)}, nil
}

// DeleteSession 删除会话（属主校验，已删除幂等成功）。
func (g *GrpcServer) DeleteSession(ctx context.Context, req *agentv1.DeleteSessionRequest) (*agentv1.DeleteSessionResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	if err := g.svc.DeleteSession(ctx, userID, sessionID); err != nil {
		return nil, err
	}
	return &agentv1.DeleteSessionResponse{}, nil
}

// ListMessages 列出会话全部消息（seq 升序，属主校验）。
func (g *GrpcServer) ListMessages(ctx context.Context, req *agentv1.ListMessagesRequest) (*agentv1.ListMessagesResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	msgs, err := g.svc.ListMessages(ctx, userID, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]*agentv1.Message, 0, len(msgs))
	for _, m := range msgs {
		protoMsg, err := toProtoMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, protoMsg)
	}
	return &agentv1.ListMessagesResponse{Messages: out}, nil
}

func (g *GrpcServer) DeleteMessage(ctx context.Context, req *agentv1.DeleteMessageRequest) (*agentv1.DeleteMessageResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	messageID, err := parseMessageID(req.MessageId)
	if err != nil {
		return nil, err
	}
	if err := g.svc.DeleteMessage(ctx, userID, sessionID, messageID); err != nil {
		return nil, err
	}
	g.log.Info("grpc DeleteMessage",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.Int64("message_id", messageID),
		zap.String("request_id", apperr.RequestIDFromContext(ctx)),
	)
	return &agentv1.DeleteMessageResponse{}, nil
}

// RenameSession 重命名会话。
func (g *GrpcServer) RenameSession(ctx context.Context, req *agentv1.RenameSessionRequest) (*agentv1.RenameSessionResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	sess, err := g.svc.RenameSession(ctx, userID, sessionID, req.Title)
	if err != nil {
		return nil, err
	}
	return &agentv1.RenameSessionResponse{Session: toProtoSession(sess)}, nil
}

// UpdateSessionConfig 更新会话配置（工具权限 / 思考模式，属主校验）。
func (g *GrpcServer) UpdateSessionConfig(ctx context.Context, req *agentv1.UpdateSessionConfigRequest) (*agentv1.UpdateSessionConfigResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	sess, err := g.svc.UpdateSessionConfig(ctx, userID, sessionID, fromProtoConfig(req.Config))
	if err != nil {
		return nil, err
	}
	return &agentv1.UpdateSessionConfigResponse{Session: toProtoSession(sess)}, nil
}

// ListTools 列出默认可用工具集（名称 + 描述），供"工具权限"配置 UI 使用。
// req.AgentId 非空时返回对应智能体域的装配视图（多智能体切换）。
func (g *GrpcServer) ListTools(ctx context.Context, req *agentv1.ListToolsRequest) (*agentv1.ListToolsResponse, error) {
	list := g.svc.ListTools(req.GetAgentId())
	out := make([]*agentv1.ToolInfo, 0, len(list))
	for _, t := range list {
		out = append(out, &agentv1.ToolInfo{Name: t.Name, Description: t.Description, External: t.External})
	}
	return &agentv1.ListToolsResponse{Tools: out}, nil
}

// ListResources 列出普通用户可见的资源清单（能力 + 技能）。
// 阶段1·权限分层：只暴露 id/名称/说明，不含工具名与技能代码。
// req.AgentId 非空时返回对应智能体域的资源清单（多智能体切换）。
func (g *GrpcServer) ListResources(ctx context.Context, req *agentv1.ListResourcesRequest) (*agentv1.ListResourcesResponse, error) {
	list := g.svc.ListResources(req.GetAgentId())
	out := make([]*agentv1.ResourceInfo, 0, len(list))
	for _, r := range list {
		out = append(out, &agentv1.ResourceInfo{
			Id:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Type:        r.Type,
		})
	}
	return &agentv1.ListResourcesResponse{Resources: out}, nil
}

// Regenerate 重新生成某轮回答（多版本保留，可切换）。
func (g *GrpcServer) Regenerate(ctx context.Context, req *agentv1.RegenerateRequest) (*agentv1.RegenerateResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	messageID, err := parseMessageID(req.MessageId)
	if err != nil {
		return nil, err
	}
	out, version, err := g.svc.Regenerate(ctx, userID, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	content := ""
	if out.Message != nil {
		content = out.Message.Content
	}
	return &agentv1.RegenerateResponse{
		Content:     content,
		Rounds:      int32(out.Rounds),
		ToolCalls:   int32(out.ToolCalls),
		TotalTokens: int64(out.Usage.TotalTokens),
		Version:     int32(version),
	}, nil
}

// SetActiveVersion 切换某轮活跃版本。
func (g *GrpcServer) SetActiveVersion(ctx context.Context, req *agentv1.SetActiveVersionRequest) (*agentv1.SetActiveVersionResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	messageID, err := parseMessageID(req.MessageId)
	if err != nil {
		return nil, err
	}
	if err := g.svc.SetActiveVersion(ctx, userID, sessionID, messageID, int(req.Version)); err != nil {
		return nil, err
	}
	return &agentv1.SetActiveVersionResponse{}, nil
}

// CreateBranch 基于当前上下文创建分支会话。
func (g *GrpcServer) CreateBranch(ctx context.Context, req *agentv1.CreateBranchRequest) (*agentv1.CreateBranchResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	messageID, err := parseMessageID(req.MessageId)
	if err != nil {
		return nil, err
	}
	sess, err := g.svc.CreateBranch(ctx, userID, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	return &agentv1.CreateBranchResponse{Session: toProtoSession(sess)}, nil
}

// ---------------------------------------------------------------------------
// 对话
// ---------------------------------------------------------------------------

// Chat 非流式对话。
func (g *GrpcServer) Chat(ctx context.Context, req *agentv1.ChatRequest) (*agentv1.ChatResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	out, err := g.svc.Chat(ctx, userID, sessionID, req.Content)
	if err != nil {
		return nil, err
	}
	return &agentv1.ChatResponse{
		Content:          out.Message.Content,
		Rounds:           int32(out.Rounds),
		ToolCalls:        int32(out.ToolCalls),
		PromptTokens:     int64(out.Usage.PromptTokens),
		CompletionTokens: int64(out.Usage.CompletionTokens),
		TotalTokens:      int64(out.Usage.TotalTokens),
	}, nil
}

// StreamChat 流式对话：文本增量逐事件下发，结束事件携带统计。
//
// 客户端断连时 stream.Context() 会取消，RunStream 内部 ChatStream 随之
// 中断并返回错误（Send 失败的诊断由 zap 记录）。
func (g *GrpcServer) StreamChat(req *agentv1.StreamChatRequest, stream grpc.ServerStreamingServer[agentv1.StreamChatEvent]) error {
	ctx := stream.Context()
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return err
	}
	// 空 content 放行（纯文件场景由 Service 层规范化，见 normalizeChatInput）。

	result, err := g.svc.StreamChatEvents(ctx, userID, sessionID, req.Content, &agent.StreamObserver{
		// 回答增量 → Delta.content
		OnContent: func(delta string) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Content: delta}},
			}); err != nil {
				// 客户端已断连：记录后继续（RunStream 会因 ctx 取消而终止）。
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 思考增量 → Delta.reasoning（前端"思考过程"气泡）
		OnReasoning: func(delta string) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Reasoning: delta}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 工具调用开始 → ToolCall 事件
		OnToolCall: func(call schema.ToolCall) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_ToolCall{ToolCall: &agentv1.ToolCall{
					ToolCallId: call.ID,
					Name:       call.Name,
					Arguments:  string(call.Arguments),
				}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 工具返回 → ToolResult 事件（成功带内容，失败带原因）
		OnToolResult: func(call schema.ToolCall, result *schema.ToolResult, execErr error) {
			tc := &agentv1.ToolResult{ToolCallId: call.ID, Name: call.Name}
			if execErr != nil {
				tc.Error = execErr.Error()
			} else if result != nil {
				tc.Content = result.Content
			}
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_ToolResult{ToolResult: tc},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 多智能体编排进度 → TaskStatus 事件（仅 mode=orchestrate 触发）
		OnTaskStatus: func(ev agent.TaskStatusEvent) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_TaskStatus{TaskStatus: &agentv1.TaskStatus{
					Type:        ev.Type,
					TaskId:      ev.TaskID,
					Status:      ev.Status,
					Error:       ev.Error,
					TotalTokens: ev.TotalTokens,
					Content:     ev.Content, // task_content 事件：子任务输出增量
					Kind:        ev.Kind,    // task_content 事件：text|reasoning|tool_start|tool_end
				}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
	})
	if err != nil {
		// 先通知客户端失败结束，再返回错误状态码。
		_ = stream.Send(&agentv1.StreamChatEvent{
			Event: &agentv1.StreamChatEvent_Done{Done: &agentv1.Done{Error: err.Error()}},
		})
		return err
	}
	return stream.Send(&agentv1.StreamChatEvent{
		Event: &agentv1.StreamChatEvent_Done{Done: &agentv1.Done{
			Rounds:           int32(result.Rounds),
			ToolCalls:        int32(result.ToolCalls),
			PromptTokens:     int64(result.Usage.PromptTokens),
			CompletionTokens: int64(result.Usage.CompletionTokens),
			TotalTokens:      int64(result.Usage.TotalTokens),
		}},
	})
}

// StreamRegenerate 流式重新生成某轮回答：正文/思考/工具/编排进度逐事件
// 下发（版本语义同 Regenerate），结束事件携带统计。
//
// 事件映射与 StreamChat 完全一致（同一 proto 流类型）；客户端断连时
// stream.Context() 取消，Service 层随 ctx 中断并返回错误。
func (g *GrpcServer) StreamRegenerate(req *agentv1.RegenerateRequest, stream grpc.ServerStreamingServer[agentv1.StreamChatEvent]) error {
	ctx := stream.Context()
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return err
	}
	messageID, err := parseMessageID(req.MessageId)
	if err != nil {
		return err
	}

	result, _, err := g.svc.StreamRegenerate(ctx, userID, sessionID, messageID, &agent.StreamObserver{
		// 回答增量 → Delta.content
		OnContent: func(delta string) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Content: delta}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 思考增量 → Delta.reasoning（前端"思考过程"气泡）
		OnReasoning: func(delta string) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_Delta{Delta: &agentv1.Delta{Reasoning: delta}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 工具调用开始 → ToolCall 事件
		OnToolCall: func(call schema.ToolCall) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_ToolCall{ToolCall: &agentv1.ToolCall{
					ToolCallId: call.ID,
					Name:       call.Name,
					Arguments:  string(call.Arguments),
				}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 工具返回 → ToolResult 事件（成功带内容，失败带原因）
		OnToolResult: func(call schema.ToolCall, result *schema.ToolResult, execErr error) {
			tc := &agentv1.ToolResult{ToolCallId: call.ID, Name: call.Name}
			if execErr != nil {
				tc.Error = execErr.Error()
			} else if result != nil {
				tc.Content = result.Content
			}
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_ToolResult{ToolResult: tc},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
		// 多智能体编排进度 → TaskStatus 事件（仅 mode=orchestrate 触发）
		OnTaskStatus: func(ev agent.TaskStatusEvent) {
			if err := stream.Send(&agentv1.StreamChatEvent{
				Event: &agentv1.StreamChatEvent_TaskStatus{TaskStatus: &agentv1.TaskStatus{
					Type:        ev.Type,
					TaskId:      ev.TaskID,
					Status:      ev.Status,
					Error:       ev.Error,
					TotalTokens: ev.TotalTokens,
					Content:     ev.Content,
					Kind:        ev.Kind,
				}},
			}); err != nil {
				g.log.Debug("stream send failed", zap.Error(err))
			}
		},
	})
	if err != nil {
		// 先通知客户端失败结束，再返回错误状态码。
		_ = stream.Send(&agentv1.StreamChatEvent{
			Event: &agentv1.StreamChatEvent_Done{Done: &agentv1.Done{Error: err.Error()}},
		})
		return err
	}
	return stream.Send(&agentv1.StreamChatEvent{
		Event: &agentv1.StreamChatEvent_Done{Done: &agentv1.Done{
			Rounds:           int32(result.Rounds),
			ToolCalls:        int32(result.ToolCalls),
			PromptTokens:     int64(result.Usage.PromptTokens),
			CompletionTokens: int64(result.Usage.CompletionTokens),
			TotalTokens:      int64(result.Usage.TotalTokens),
		}},
	})
}

// SubmitToolResult 回填外部工具执行结果（阶段3·本地工具代理）。
// 桌面客户端在完成本地工具执行后调用，唤醒挂起等待的会话继续推理。
func (g *GrpcServer) SubmitToolResult(ctx context.Context, req *agentv1.SubmitToolResultRequest) (*agentv1.SubmitToolResultResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	sessionID, err := parseSessionID(req.SessionId)
	if err != nil {
		return nil, err
	}
	if req.ToolCallId == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "缺少工具调用标识")
	}
	result := &schema.ToolResult{
		Content: req.Content,
		IsError: req.IsError,
	}
	if err := g.svc.SubmitToolResult(ctx, userID, sessionID, req.ToolCallId, result); err != nil {
		return nil, err
	}
	return &agentv1.SubmitToolResultResponse{}, nil
}

// MergeGuestSessions 登录后把游客会话合并到当前账号（阶段2·游客模式）。
// 仅真实用户（user_id > 0）可调用；游客身份无法作为合并目标。
func (g *GrpcServer) MergeGuestSessions(ctx context.Context, req *agentv1.MergeGuestSessionsRequest) (*agentv1.MergeGuestSessionsResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	n, err := g.svc.MergeGuestSessions(ctx, userID, req.GuestId)
	if err != nil {
		return nil, err
	}
	g.log.Info("grpc MergeGuestSessions",
		zap.Int64("user_id", userID),
		zap.Int("migrated", n),
		zap.String("request_id", apperr.RequestIDFromContext(ctx)),
	)
	return &agentv1.MergeGuestSessionsResponse{Migrated: int32(n)}, nil
}

// UploadChatDocument 聊天上传文档（模块二）：解析 → 落工作区 → 注入历史。
func (g *GrpcServer) UploadChatDocument(ctx context.Context, req *agentv1.UploadChatDocumentRequest) (*agentv1.UploadChatDocumentResponse, error) {
	userID, err := userIDFromMetadata(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.SessionId == "" {
		return nil, apperr.New(apperr.CodeInvalidArgument, "缺少会话 ID")
	}
	sessionID, err := strconv.ParseInt(req.SessionId, 10, 64)
	if err != nil || sessionID <= 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "会话 ID 非法")
	}
	res, err := g.svc.UploadChatDocument(ctx, userID, sessionID, req.FileName, req.Content)
	if err != nil {
		return nil, err
	}
	g.log.Info("grpc UploadChatDocument",
		zap.Int64("user_id", userID),
		zap.Int64("session_id", sessionID),
		zap.String("file", res.FileName),
		zap.Int("segments", res.Segments),
		zap.String("request_id", apperr.RequestIDFromContext(ctx)),
	)
	return &agentv1.UploadChatDocumentResponse{
		FileName:    res.FileName,
		RelPath:     res.RelPath,
		Segments:    int32(res.Segments),
		InjectedLen: int32(res.InjectedLen),
		Media:       res.Media,
		Warnings:    res.Warnings,
	}, nil
}

// AdminSessionStats 管理端会话统计（数据管理模块）。
// 由 gateway adminsvc 经鉴权后转发（内网可信）；本层仅校验身份存在
// （防裸 gRPC 匿名直连，8082 端口暴露宿主）与窗口参数合法性。
func (g *GrpcServer) AdminSessionStats(ctx context.Context, req *agentv1.AdminSessionStatsRequest) (*agentv1.AdminSessionStatsResponse, error) {
	if req == nil {
		return nil, apperr.New(apperr.CodeInvalidArgument, "请求体不能为空")
	}
	if _, err := userIDFromMetadata(ctx); err != nil {
		return nil, err
	}
	days := int(req.Days)
	if days == 0 {
		days = 30 // 缺省窗口：近 30 天
	}
	st, err := g.svc.AdminSessionStats(ctx, days)
	if err != nil {
		return nil, err
	}
	resp := &agentv1.AdminSessionStatsResponse{TotalSessions: st.Total}
	for _, d := range st.Daily {
		resp.Days = append(resp.Days, &agentv1.SessionDayStat{Date: d.Date, Sessions: d.Sessions})
	}
	for _, a := range st.ByAgent {
		resp.Agents = append(resp.Agents, &agentv1.SessionAgentStat{AgentId: a.AgentID, Sessions: a.Sessions})
	}
	g.log.Info("grpc AdminSessionStats",
		zap.Int("days", days),
		zap.Int64("total_sessions", st.Total),
		zap.String("request_id", apperr.RequestIDFromContext(ctx)),
	)
	return resp, nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// userIDFromMetadata 从 gRPC metadata 读取 x-user-id。
// 缺失/非法一律拒绝——匿名请求无法归属会话与用量。
// 合法取值：真实用户 > 0；游客（阶段2）为 auth.GuestUserID 派生的负值，仅 0 非法。
func userIDFromMetadata(ctx context.Context) (int64, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return 0, apperr.New(apperr.CodeUnauthenticated, "缺少用户身份")
	}
	vals := md.Get(metadataUserID)
	if len(vals) == 0 || vals[0] == "" {
		return 0, apperr.New(apperr.CodeUnauthenticated, "缺少用户身份")
	}
	uid, err := strconv.ParseInt(vals[0], 10, 64)
	if err != nil || uid == 0 {
		return 0, apperr.New(apperr.CodeInvalidArgument, "非法的用户身份")
	}
	return uid, nil
}

// userRoleFromMetadata 从 gRPC metadata 读取调用方角色；缺失返回空串。
// 空串按普通用户处理（llm-gateway 侧 isAdminRole("") == false）。
func userRoleFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(metadataUserRole)
	if len(vals) == 0 || vals[0] == "" {
		return ""
	}
	return vals[0]
}

// withUserHeaders 把调用方身份注入上游请求头：X-User-Id（必填）与
// X-User-Role（metadata 提供时）。llm-gateway 依据角色决定配额默认值
// （管理员免配额），缺失按普通用户处理。
func withUserHeaders(ctx context.Context, userID int64) context.Context {
	runCtx := llm.WithHeader(ctx, "X-User-Id", strconv.FormatInt(userID, 10))
	if role := userRoleFromMetadata(ctx); role != "" {
		runCtx = llm.WithHeader(runCtx, "X-User-Role", role)
	}
	return runCtx
}

// parseSessionID 解析 proto 的字符串会话 ID。
func parseSessionID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.New(apperr.CodeInvalidArgument, "非法的会话 ID")
	}
	return id, nil
}

// parseMessageID 解析消息 ID（数据库主键，正数）。
func parseMessageID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.New(apperr.CodeInvalidArgument, "非法的消息 ID")
	}
	return id, nil
}

// toProtoSession 领域模型 → proto 模型。
func toProtoSession(s *Session) *agentv1.Session {
	cfg := &agentv1.SessionConfig{}
	// 显式清空的资源选择（set 标记）也要透传：空数组 = 不启用任何能力/技能，
	// 与"未设置（全部启用）"不可混为一谈（见 model.go EnabledResourcesSet 注释）。
	if len(s.Config.EnabledResources) > 0 || s.Config.EnabledResourcesSet {
		cfg.EnabledResources = s.Config.EnabledResources
		cfg.EnabledResourcesSet = s.Config.EnabledResourcesSet
	}
	if len(s.Config.EnabledTools) > 0 {
		cfg.EnabledTools = s.Config.EnabledTools
	}
	if len(s.Config.KBIDs) > 0 {
		cfg.KbIds = s.Config.KBIDs
	}
	if len(s.Config.MCPServers) > 0 {
		cfg.McpServers = s.Config.MCPServers
	}
	if s.Config.MCPServersSet {
		cfg.McpServersSet = true
	}
	if s.Config.KBIDsSet {
		cfg.KbIdsSet = true
	}
	// 管理员级字段（快照固化）一律透传（0 = 未设置，装配层回退实例默认）。
	cfg.MaxRounds = int32(s.Config.MaxRounds)
	cfg.MaxMessages = int32(s.Config.MaxMessages)
	cfg.MaxThinkingRounds = int32(s.Config.MaxThinkingRounds)
	// 会话选定模型（普通可配字段；空 = 回退默认）。
	cfg.Model = s.Config.Model
	// 能力/技能独立 presence 标记（P3 反馈，透传给 gateway → 前端配置区）。
	cfg.EnabledCapabilitiesSet = s.Config.EnabledCapabilitiesSet
	cfg.EnabledSkillsSet = s.Config.EnabledSkillsSet
	// 会话运行模式（single | orchestrate）。
	cfg.Mode = s.Config.Mode
	// 编排方案（fixed | dynamic，仅 orchestrate 生效）。
	cfg.OrchestratePlan = s.Config.OrchestratePlan
	if s.Config.Thinking != nil {
		cfg.Thinking = &agentv1.ThinkingConfig{
			Enabled:         s.Config.Thinking.Enabled,
			ReasoningEffort: s.Config.Thinking.ReasoningEffort,
		}
	}
	return &agentv1.Session{
		Id:        strconv.FormatInt(s.ID, 10),
		UserId:    strconv.FormatInt(s.UserID, 10),
		Title:     s.Title,
		CreatedAt: timestamppb.New(s.CreatedAt),
		UpdatedAt: timestamppb.New(s.UpdatedAt),
		Config:    cfg,
		AgentId:   s.AgentID,
	}
}

// fromProtoConfig proto 配置 → 领域配置（nil 视为空配置）。
func fromProtoConfig(c *agentv1.SessionConfig) SessionConfig {
	if c == nil {
		return SessionConfig{}
	}
	cfg := SessionConfig{
		EnabledResources:       c.EnabledResources,
		EnabledResourcesSet:    c.EnabledResourcesSet,
		EnabledTools:           c.EnabledTools,
		KBIDs:                  c.KbIds,
		KBIDsSet:               c.KbIdsSet,
		MCPServers:             c.McpServers,
		MCPServersSet:          c.McpServersSet,
		MaxRounds:              int(c.MaxRounds),
		MaxMessages:            int(c.MaxMessages),
		MaxThinkingRounds:      int(c.MaxThinkingRounds),
		Model:                  c.Model,
		EnabledCapabilitiesSet: c.EnabledCapabilitiesSet,
		EnabledSkillsSet:       c.EnabledSkillsSet,
		Mode:                   c.Mode,
		OrchestratePlan:        c.OrchestratePlan,
	}
	if c.Thinking != nil {
		cfg.Thinking = &ThinkingConfig{
			Enabled:         c.Thinking.Enabled,
			ReasoningEffort: c.Thinking.ReasoningEffort,
		}
	}
	return cfg
}

// toProtoMessage 领域消息 → proto 消息（tool_calls 序列化为 JSON 字符串，
// 便于前端直接展示，避免把 framework 类型耦合进契约层）。
func toProtoMessage(m *Message) (*agentv1.Message, error) {
	var toolCalls string
	if len(m.ToolCalls) > 0 {
		b, err := json.Marshal(m.ToolCalls)
		if err != nil {
			return nil, apperr.Wrap(apperr.CodeInternal, "序列化 tool_calls 失败", err)
		}
		toolCalls = string(b)
	}
	return &agentv1.Message{
		Role:          m.Role,
		Content:       m.Content,
		Reasoning:     m.Reasoning,
		ToolCallId:    m.ToolCallID,
		ToolCalls:     toolCalls,
		Id:            strconv.FormatInt(m.ID, 10),
		RoundNo:       m.RoundNo,
		Version:       int32(m.Version),
		TotalVersions: int32(m.TotalVersions),
	}, nil
}
