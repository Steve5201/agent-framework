// Package grpcx 提供 gRPC 服务端/客户端封装与统一拦截器（P2-14）：
//   - 服务端拦截器链：request_id 注入 context → panic 恢复 → 结构化日志；
//   - 客户端拦截器：将 context 中的 request_id 注入出站 metadata（全链路透传）；
//   - 鉴权拦截器占位（AuthPlaceholder），正式实现见 P2-B auth-service。
//
// request_id 在 HTTP 与 gRPC 之间统一采用小写 metadata 键 x-request-id 传递。
//
// 使用示例（服务端）：
//
//	srv := grpcx.NewServer(log)
//	authpb.RegisterAuthServiceServer(srv, authSvc)
//	lis, _ := net.Listen("tcp", ":8081")
//	srv.Serve(lis)
//
// 使用示例（客户端）：
//
//	ctx = errors.NewContextWithRequestID(ctx, reqID) // 上游注入
//	conn, _ := grpcx.Dial(ctx, "auth:8081")
//	client := authpb.NewAuthServiceClient(conn)
package grpcx

import (
	"context"
	"net"
	"time"

	"github.com/Steve5201/agent-backend/internal/errors"
	"github.com/Steve5201/agent-backend/internal/reqid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// metadataRequestID request_id 在 gRPC metadata 中的键（小写）。
const metadataRequestID = "x-request-id"

// maxMsgSize gRPC 单条消息上限（64MB）。
// 默认 4MB 会阻断大文件文档上传（聊天文档/RAG 摄取原始文件字节随 RPC 传输，
// 网关侧单文件上限 20MB）；放宽到 64MB 为多种 PDF/PPT 留足余量
// （二进制字节略大于源文件、多文档批量场景也不受影响）。
const maxMsgSize = 64 << 20

// ---------------------------------------------------------------------------
// 服务端
// ---------------------------------------------------------------------------

// NewServer 创建带统一拦截器链的 gRPC 服务端。
// 拦截器顺序：RequestID（最先，注入 context）→ Recovery → Logging。
// 业务自定义拦截器通过 extra 追加在链尾（最靠近 handler）。
func NewServer(log *zap.Logger, extra ...grpc.UnaryServerInterceptor) *grpc.Server {
	ints := []grpc.UnaryServerInterceptor{
		UnaryRequestID(),
		UnaryRecovery(log),
		UnaryLogging(log),
	}
	ints = append(ints, extra...)
	return grpc.NewServer(
		grpc.ChainUnaryInterceptor(ints...),
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
	)
}

// Serve 在指定地址监听并服务，阻塞直到 Stop/GracefulStop。
func Serve(srv *grpc.Server, lis net.Listener, log *zap.Logger, name string) error {
	log.Info("gRPC service listening", zap.String("service", name), zap.String("addr", lis.Addr().String()))
	if err := srv.Serve(lis); err != nil {
		return status.Errorf(codes.Internal, "gRPC serve: %v", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 客户端
// ---------------------------------------------------------------------------

// Dial 创建到目标地址的 gRPC 客户端连接。
// 自动注入 request_id 透传拦截器（从 context 读取，写入出站 metadata）。
// 使用 grpc.NewClient 懒连接（不阻塞建立真实连接），适合微服务启动时序。
func Dial(ctx context.Context, addr string, extra ...grpc.UnaryClientInterceptor) (*grpc.ClientConn, error) {
	ints := []grpc.UnaryClientInterceptor{UnaryClientRequestID()}
	ints = append(ints, extra...)
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(ints...),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMsgSize),
			grpc.MaxCallSendMsgSize(maxMsgSize),
		),
	)
}

// ---------------------------------------------------------------------------
// 拦截器
// ---------------------------------------------------------------------------

// UnaryRequestID 服务端拦截器：从入站 metadata 读取 x-request-id 写入 context；
// 缺失时生成新 ID。保证后续拦截器与业务 handler 总能拿到 request_id。
func UnaryRequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get(metadataRequestID); len(v) > 0 {
				id = v[0]
			}
		}
		if id == "" {
			id = reqid.Generate()
		}
		return handler(errors.NewContextWithRequestID(ctx, id), req)
	}
}

// UnaryRecovery 服务端拦截器：捕获 handler panic，记录堆栈并返回
// codes.Internal，避免进程崩溃与内部细节泄露。
func UnaryRecovery(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (out any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("gRPC panic recovered",
					zap.Any("panic", rec),
					zap.String("method", info.FullMethod),
					zap.String("request_id", errors.RequestIDFromContext(ctx)),
					zap.Stack("stack"),
				)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// UnaryLogging 服务端拦截器：记录每次 RPC 的方法、耗时、状态码与 request_id。
func UnaryLogging(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		out, err := handler(ctx, req)
		log.Info("gRPC request",
			zap.String("method", info.FullMethod),
			zap.Int64("duration_ms", time.Since(start).Milliseconds()),
			zap.String("code", status.Code(err).String()),
			zap.String("request_id", errors.RequestIDFromContext(ctx)),
		)
		return out, err
	}
}

// UnaryClientRequestID 客户端拦截器：将 context 中的 request_id 写入出站
// metadata，实现 HTTP(gateway) → gRPC(服务) → HTTP(llm) 的全链路透传。
func UnaryClientRequestID() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if id := errors.RequestIDFromContext(ctx); id != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, metadataRequestID, id)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// AuthPlaceholder 鉴权拦截器占位（未启用）。
// 正式实现计划：从入站 metadata 读取 Authorization，解析 JWT，
// 将 user_id/role 注入 context（见 P2-B auth-service 与 P2-12 auth 包）。
// 返回 nil 表示放行；当前占位不拦截任何请求。
func AuthPlaceholder(log *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(ctx, req)
	}
}
