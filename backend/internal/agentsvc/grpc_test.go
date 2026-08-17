// grpc_test.go —— agent-service gRPC 传输层单测（P2-46）。
//
// 覆盖：x-user-id 注入的缺失/非法/合法三种路径；会话 ID 解析。
package agentsvc

import (
	"context"
	"testing"

	apperr "github.com/Steve5201/agent-backend/internal/errors"
	"google.golang.org/grpc/metadata"
)

func TestUserIDFromMetadata(t *testing.T) {
	cases := []struct {
		name string
		md   metadata.MD
		want int64
		code apperr.ErrorCode
	}{
		{"合法", metadata.Pairs(metadataUserID, "42"), 42, ""},
		{"游客负数合法", metadata.Pairs(metadataUserID, "-3700210294184851756"), -3700210294184851756, ""},
		{"缺失 metadata", nil, 0, apperr.CodeUnauthenticated},
		{"空值", metadata.Pairs(metadataUserID, ""), 0, apperr.CodeUnauthenticated},
		{"非数字", metadata.Pairs(metadataUserID, "abc"), 0, apperr.CodeInvalidArgument},
		{"零非法", metadata.Pairs(metadataUserID, "0"), 0, apperr.CodeInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			if c.md != nil {
				ctx = metadata.NewIncomingContext(ctx, c.md)
			}
			got, err := userIDFromMetadata(ctx)
			if c.code == "" {
				if err != nil || got != c.want {
					t.Fatalf("期望成功且 user_id=%d, got %d err=%v", c.want, got, err)
				}
				return
			}
			if code := apperr.CodeOf(err); code != c.code {
				t.Fatalf("期望错误码 %s, got %s", c.code, code)
			}
		})
	}
}

func TestParseSessionID(t *testing.T) {
	if _, err := parseSessionID(""); err == nil {
		t.Fatal("空 ID 应报错")
	}
	if _, err := parseSessionID("0"); err == nil {
		t.Fatal("0 应报错")
	}
	if _, err := parseSessionID("abc"); err == nil {
		t.Fatal("非数字应报错")
	}
	id, err := parseSessionID("7")
	if err != nil || id != 7 {
		t.Fatalf("合法 ID 解析失败: %d %v", id, err)
	}
}
