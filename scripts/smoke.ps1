# 端到端冒烟测试（P2-63/64/65）：验证全链路真实跑通。
#
# 覆盖链路：
#   注册 → 登录 → me → 刷新令牌 → 创建会话 → 会话列表 → 会话详情
#   → 非流式对话 → SSE 流式对话（增量到达）→ request_id 全链路 → 登出
#
# 前置：全栈已启动（.\scripts\dev-up.ps1）。
# 依赖真实模型（DEEPSEEK_API_KEY）：对话阶段会调用 DeepSeek，会产生少量费用。
# 用法：.\scripts\smoke.ps1 [-SkipModel]   （-SkipModel 跳过对话阶段）
param(
    [switch]$SkipModel
)

$ErrorActionPreference = 'Stop'
$base = 'http://localhost:8080'
$repoRoot = Join-Path $PSScriptRoot '..'
$composeFile = Join-Path $repoRoot 'deploy\docker-compose.yml'

# 唯一用户名 + 唯一 request_id（用于全链路日志追踪）。
$rand = Get-Random -Minimum 100000 -Maximum 999999
$username = "smoke_$rand"
$password = 'smoke123456'
$rid = 'smoke-' + [guid]::NewGuid().ToString('N').Substring(0, 12)
$globalRid = $rid   # request_id 全链路复用同一值，便于日志 grep

function Write-Step([string]$msg) {
    Write-Host "`n=== $msg ===" -ForegroundColor Cyan
}

# 请求封装：JSON 请求，自动带 request_id 头；失败时打印详情并退出。
function Invoke-Api {
    param(
        [string]$Method,
        [string]$Uri,
        $Body,
        [hashtable]$Auth,
        [string]$ContentType = 'application/json; charset=utf-8'
    )
    $headers = @{ 'X-Request-Id' = $globalRid }
    if ($Auth) { foreach ($k in $Auth.Keys) { $headers[$k] = $Auth[$k] } }
    try {
        $params = @{
            Method      = $Method
            Uri         = "$base$Uri"
            Headers     = $headers
            ContentType = $ContentType
            UseBasicParsing = $true
        }
        if ($null -ne $Body) { $params.Body = $Body | ConvertTo-Json -Compress -Depth 5 }
        return Invoke-RestMethod @params
    }
    catch {
        Write-Host "[失败] $Method $Uri" -ForegroundColor Red
        Write-Host "   $($_.Exception.Message)" -ForegroundColor Red
        if ($_.ErrorDetails.Message) { Write-Host "   body: $($_.ErrorDetails.Message)" -ForegroundColor Red }
        exit 1
    }
}

function Assert-Equal {
    param($Name, $Actual, $Expected)
    if ($Actual -ne $Expected) {
        Write-Host "[失败] $Name 期望 $Expected 实际 $Actual" -ForegroundColor Red
        exit 1
    }
}

# ---- 0. 健康检查 ----
Write-Step '0/10 健康检查'
$h = Invoke-WebRequest "$base/healthz" -UseBasicParsing -TimeoutSec 5
Assert-Equal 'healthz 状态码' $h.StatusCode 200
Write-Host '[OK] gateway 存活' -ForegroundColor Green

# ---- 1. 注册 ----
# 裸 /v1/auth/register 已下线（管理员只能被创建）；匿名自助注册走分智能体入口
# /v1/auth/register/{agent_id}（agent_id 由 gateway 解析并写入账号标签）。
Write-Step '1/10 注册'
$reg = Invoke-Api -Method POST -Uri '/v1/auth/register/default' -Body @{ username = $username; password = $password }
if (-not $reg.user_id) { Write-Host '[失败] 注册未返回 user_id' -ForegroundColor Red; exit 1 }
Write-Host "[OK] 注册成功 user_id=$($reg.user_id)" -ForegroundColor Green

# ---- 2. 登录 ----
# 裸 /v1/auth/login 仅限管理员；普通用户走分智能体入口
# /v1/auth/login/{agent_id}（与注册入口保持一致）。
Write-Step '2/10 登录'
$login = Invoke-Api -Method POST -Uri '/v1/auth/login/default' -Body @{ username = $username; password = $password }
$token = [string]$login.access_token
if (-not $token) { Write-Host '[失败] 登录未返回 access_token' -ForegroundColor Red; exit 1 }
$authH = @{ Authorization = "Bearer $token" }
Write-Host '[OK] 登录成功，拿到 access_token' -ForegroundColor Green

# ---- 3. 当前用户 ----
Write-Step '3/10 me（验证 JWT 生效）'
$me = Invoke-Api -Method GET -Uri '/v1/auth/me' -Auth $authH
if ($me.username -ne $username) {
    Write-Host "[失败] me.username=$($me.username) 期望 $username" -ForegroundColor Red
    exit 1
}
Write-Host "[OK] me.username=$($me.username)" -ForegroundColor Green

# ---- 4. 刷新令牌 ----
Write-Step '4/10 刷新令牌'
$ref = Invoke-Api -Method POST -Uri '/v1/auth/refresh' -Body @{ refresh_token = $login.refresh_token }
if (-not $ref.access_token) { Write-Host '[失败] 刷新未返回 access_token' -ForegroundColor Red; exit 1 }
Write-Host '[OK] 刷新成功（旧 refresh 已一次性吊销）' -ForegroundColor Green

# ---- 5. 创建会话 ----
Write-Step '5/10 创建会话'
$sess = Invoke-Api -Method POST -Uri '/v1/agent/sessions' -Body @{ title = '冒烟测试会话' } -Auth $authH
$sid = [string]$sess.session.id
if (-not $sid) { Write-Host '[失败] 创建会话未返回 id' -ForegroundColor Red; exit 1 }
Write-Host "[OK] session_id=$sid" -ForegroundColor Green

# ---- 6. 会话详情 ----
# 列表断言放到对话之后（列表只返回"有过消息"的会话，见 agent-svc EXISTS 语义）。
Write-Step '6/10 会话详情'
$detail = Invoke-Api -Method GET -Uri "/v1/agent/sessions/$sid" -Auth $authH
if ($detail.session.title -ne '冒烟测试会话') {
    Write-Host '[失败] 会话详情标题不符' -ForegroundColor Red; exit 1
}
Write-Host '[OK] 详情正常' -ForegroundColor Green

if (-not $SkipModel) {
    # ---- 7. 非流式对话（真实调用模型）----
    Write-Step '7/10 非流式对话（走 agent → llm-gateway → DeepSeek）'
    $chat = Invoke-Api -Method POST -Uri "/v1/agent/sessions/$sid/chat" `
        -Body @{ content = '用一句话介绍示例大学' } -Auth $authH
    if (-not $chat.content) { Write-Host '[失败] 对话未返回内容' -ForegroundColor Red; exit 1 }
    Write-Host "[OK] 回答: $($chat.content)" -ForegroundColor Green
    Write-Host "    (rounds=$($chat.rounds) tool_calls=$($chat.tool_calls) tokens=$($chat.total_tokens))" -ForegroundColor DarkGray

    # ---- 8. SSE 流式对话 ----
    # 注意：不走 curl.exe —— PowerShell 5.1 向原生程序传参时，内嵌双引号会被
    # 剥离（-d '{"content":"hello"}' 实际发送 {content:hello}），导致 40001
    # JSON 解析失败（与字符编码无关，中文/ASCII 均受影响）。改用
    # Invoke-WebRequest（ConvertTo-Json 序列化，无此问题；SSE 响应体完整
    # 缓冲后即可检查 done/error 事件）。
    Write-Step '8/10 SSE 流式对话（验证增量到达）'
    $sseResp = Invoke-WebRequest -Method POST -Uri "$base/v1/agent/sessions/$sid/chat/stream" `
        -Headers ($authH + @{ 'X-Request-Id' = $globalRid }) `
        -ContentType 'application/json; charset=utf-8' `
        -Body (@{ content = 'hello' } | ConvertTo-Json -Compress) -UseBasicParsing
    $sse = $sseResp.Content
    if ($sse -match 'event: done') {
        Write-Host '[OK] SSE 流正常结束（含 done 事件）' -ForegroundColor Green
    } elseif ($sse -match 'event: error') {
        Write-Host "[失败] SSE 流中错误事件:`n$sse" -ForegroundColor Red
        exit 1
    } else {
        Write-Host "[失败] SSE 未收到 done/error 事件:`n$sse" -ForegroundColor Red
        exit 1
    }
} else {
    Write-Host "`n[跳过] 对话阶段（-SkipModel）——基建链路已验证" -ForegroundColor Yellow
}

# ---- 9. 会话列表（对话后） ----
# 列表只返回"有过消息"的会话（EXISTS 语义），故断言放到对话之后；
# -SkipModel 未产生消息，跳过严格断言。
Write-Step '9/10 会话列表'
if ($SkipModel) {
    Write-Host "[跳过] -SkipModel 未产生消息，不断言列表" -ForegroundColor Yellow
} else {
    $list = Invoke-Api -Method GET -Uri '/v1/agent/sessions?page=1&page_size=10' -Auth $authH
    if ($list.total -lt 1) { Write-Host '[失败] 会话列表为空' -ForegroundColor Red; exit 1 }
    $found = @($list.sessions | Where-Object { $_.id -eq $sid }).Count
    if ($found -lt 1) { Write-Host '[失败] 列表未包含本会话' -ForegroundColor Red; exit 1 }
    Write-Host '[OK] 列表正常' -ForegroundColor Green
}

# ---- 10. request_id 全链路验证 ----
Write-Step '10/10 request_id 贯穿网关→agent→llm-gateway'
$req = Invoke-WebRequest -Method GET -Uri "$base/v1/agent/sessions" `
    -Headers @{ Authorization = "Bearer $token"; 'X-Request-Id' = $globalRid } -UseBasicParsing
$echoed = $req.Headers['X-Request-Id']
if ($echoed -and "$echoed" -eq $globalRid) {
    Write-Host "[OK] gateway 响应头回显 request_id=$globalRid" -ForegroundColor Green
} else {
    Write-Host "[警告] gateway 未回显 request_id（已发=$globalRid 收到=$echoed）" -ForegroundColor Yellow
}
Write-Host "下面展示该 request_id 在各服务日志中的出现（贯穿验证）：" -ForegroundColor DarkGray
& docker compose -f $composeFile logs --no-color --since 10m 2>$null |
    Select-String -Pattern $globalRid |
    ForEach-Object { Write-Host "  $($_.Line.Substring(0, [Math]::Min($_.Line.Length, 220)))" -ForegroundColor DarkGray }

# ---- 10. 登出 ----
Write-Step '10/10 登出'
Invoke-Api -Method POST -Uri '/v1/auth/logout' -Body @{ refresh_token = $login.refresh_token } -Auth $authH
Write-Host '[OK] 登出成功' -ForegroundColor Green

Write-Host "`n🎉 端到端冒烟全部通过（user=$username, session=$sid, request_id=$globalRid）" -ForegroundColor Green
