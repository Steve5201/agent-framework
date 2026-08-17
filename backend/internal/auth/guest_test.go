package auth

import "testing"

func TestGuestUserID(t *testing.T) {
	t.Run("合法游客 ID 派生稳定负值", func(t *testing.T) {
		id := "550e8400-e29b-41d4-a716-446655440000"
		first := GuestUserID(id)
		second := GuestUserID(id)
		if first == 0 {
			t.Fatal("合法游客 ID 不应派生为 0")
		}
		if first >= 0 {
			t.Fatalf("游客 user_id 必须为负，got %d", first)
		}
		if first != second {
			t.Fatalf("同一游客 ID 派生结果必须稳定：%d != %d", first, second)
		}
		if !IsGuestUserID(first) {
			t.Fatalf("IsGuestUserID(%d) 应为 true", first)
		}
	})

	t.Run("不同游客 ID 派生不同", func(t *testing.T) {
		a := GuestUserID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
		b := GuestUserID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
		if a == b {
			t.Fatalf("不同游客 ID 不应派生相同 user_id：%d", a)
		}
	})

	t.Run("非法游客 ID 派生 0", func(t *testing.T) {
		cases := []string{"", "short", "含中文", "spaces in id", "too_long_" + string(make([]byte, 128))}
		for _, c := range cases {
			if got := GuestUserID(c); got != 0 {
				t.Fatalf("非法游客 ID %q 应派生 0，got %d", c, got)
			}
		}
	})

	t.Run("真实用户 ID 恒为正", func(t *testing.T) {
		if IsGuestUserID(1) || IsGuestUserID(123456) {
			t.Fatal("真实用户 ID 不应被判为游客")
		}
	})
}
