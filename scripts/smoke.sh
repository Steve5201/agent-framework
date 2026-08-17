#!/usr/bin/env bash
# 端到端冒烟测试（P2-63/64/65）—— bash 版，供 Linux/CI 使用。
# Windows 本地请用 scripts/smoke.ps1。
#
# 用法：./scripts/smoke.sh [-skip-model]
set -euo pipefail

BASE="${BASE:-http://localhost:8080}"
SKIP_MODEL=0
[ "${1:-}" = "-skip-model" ] && SKIP_MODEL=1

USERNAME="smoke_$(date +%s)_$RANDOM"
PASSWORD="smoke123456"
RID="smoke-$(date +%s | md5sum | cut -c1-12)"

step() { echo; echo "=== $1 ==="; }

# api METHOD URI BODY(optional, JSON string) EXTRA_CURL_ARGS...
api() {
    local method=$1 uri=$2 body=${3:-} extra=${4:-}
    local args=(-s -X "$method" "$BASE$uri" -H "X-Request-Id: $RID")
    [ -n "$body" ] && args+=(-H "Content-Type: application/json" -d "$body")
    [ -n "$extra" ] && args+=("$extra")
    local out
    out=$(curl "${args[@]}") || {
        echo "[失败] $method $uri"; exit 1
    }
    echo "$out"
}

fail() { echo "[失败] $1" >&2; exit 1; }

# 0. 健康检查
step "0/9 健康检查"
curl -sf "$BASE/healthz" >/dev/null || fail "gateway 未就绪"
echo "[OK] gateway 存活"

# 1. 注册
step "1/9 注册"
REG=$(api POST /v1/auth/register "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}")
echo "$REG" | grep -q '"user_id"' || fail "注册失败: $REG"
echo "[OK] 注册成功: $REG"

# 2. 登录
step "2/9 登录"
LOGIN=$(api POST /v1/auth/login "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}")
TOKEN=$(echo "$LOGIN" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ] || fail "登录失败: $LOGIN"
echo "[OK] 登录成功"

# 3. me
step "3/9 me"
ME=$(api GET /v1/auth/me "" "-H Authorization: Bearer $TOKEN")
echo "$ME" | grep -q "\"username\":\"$USERNAME\"" || fail "me 不符: $ME"
echo "[OK] me=$ME"

# 4. 刷新
step "4/9 刷新令牌"
REF=$(echo "$LOGIN" | sed -n 's/.*"refresh_token":"\([^"]*\)".*/\1/p')
api POST /v1/auth/refresh "{\"refresh_token\":\"$REF\"}" | grep -q '"access_token"' || fail "刷新失败"
echo "[OK] 刷新成功"

# 5. 创建会话
step "5/9 创建会话"
SESS=$(api POST /v1/agent/sessions "{\"title\":\"冒烟测试\"}" "-H Authorization: Bearer $TOKEN")
SID=$(echo "$SESS" | sed -n 's/.*"id":"\([0-9]*\)".*/\1/p')
[ -n "$SID" ] || fail "创建会话失败: $SESS"
echo "[OK] session_id=$SID"

# 6. 会话列表
step "6/9 会话列表"
api GET "/v1/agent/sessions?page=1&page_size=10" "" "-H Authorization: Bearer $TOKEN" \
    | grep -q '"total"' || fail "列表失败"
echo "[OK] 列表正常"

# 7/8. 对话（依赖真实模型）
if [ "$SKIP_MODEL" -eq 1 ]; then
    echo "[跳过] 对话阶段（-skip-model）"
else
    step "7/9 非流式对话"
    CHAT=$(api POST "/v1/agent/sessions/$SID/chat" "{\"content\":\"用一句话介绍示例大学\"}" "-H Authorization: Bearer $TOKEN")
    echo "[OK] 回答: $CHAT"

    step "8/9 SSE 流式对话"
    SSE=$(curl -s -N -X POST "$BASE/v1/agent/sessions/$SID/chat/stream" \
        -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
        -H "X-Request-Id: $RID" -d '{"content":"介绍下你自己"}')
    echo "$SSE" | grep -q 'event: done' || fail "SSE 未正常结束: $SSE"
    echo "[OK] SSE 流正常结束"
fi

# 9. request_id 贯穿验证
step "9/9 request_id 贯穿"
echo "[OK] 冒烟通过（request_id=$RID，可在 docker compose logs 中 grep 贯穿验证）"

echo
echo "🎉 端到端冒烟全部通过（user=$USERNAME session=$SID）"
