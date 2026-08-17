# verify-orchestrate.ps1 —— P4-J 动态编排端到端验证（一次性验证脚本）。
#
# 覆盖：配置 dynamic 编排方案 → SSE 流式编排对话 → task_status 进度事件 →
# done 结束事件。真实调用 DeepSeek（产生少量 API 费用）。
#
# 前置：全栈已启动（rebuild.ps1 已完成）。
# 用法：.\scripts\verify-orchestrate.ps1
param()
$ErrorActionPreference = 'Stop'
$base = 'http://localhost:8080'

$rand = Get-Random -Minimum 100000 -Maximum 999999
$username = "orch_$rand"
$password = 'smoke123456'

function Invoke-Api {
    param([string]$Method, [string]$Uri, $Body, [hashtable]$Auth)
    $headers = @{}
    if ($Auth) { foreach ($k in $Auth.Keys) { $headers[$k] = $Auth[$k] } }
    $params = @{ Method = $Method; Uri = "$base$Uri"; Headers = $headers; UseBasicParsing = $true }
    if ($null -ne $Body) { $params.ContentType = 'application/json; charset=utf-8'; $params.Body = $Body | ConvertTo-Json -Compress -Depth 6 }
    return Invoke-RestMethod @params
}
function Fail([string]$m) { Write-Host "[失败] $m" -ForegroundColor Red; exit 1 }

Write-Host '=== 1/5 注册+登录 ===' -ForegroundColor Cyan
Invoke-Api -Method POST -Uri '/v1/auth/register/default' -Body @{ username = $username; password = $password } | Out-Null
$login = Invoke-Api -Method POST -Uri '/v1/auth/login/default' -Body @{ username = $username; password = $password }
$token = [string]$login.access_token
$authH = @{ Authorization = "Bearer $token" }
Write-Host "[OK] 登录成功 $username" -ForegroundColor Green

Write-Host '=== 2/5 创建会话 + 配置 dynamic 编排 ===' -ForegroundColor Cyan
$sess = Invoke-Api -Method POST -Uri '/v1/agent/sessions' -Body @{ title = '动态编排验证' } -Auth $authH
$sid = [string]$sess.session.id
if (-not $sid) { Fail '会话创建失败' }

# 配置：mode=orchestrate + orchestrate_plan=dynamic（同时启用 docgen 能力白名单，
# 使编排子任务可装配文档生成工具，验证 P4-J 子任务工具化）。
$cfg = @{ mode = 'orchestrate'; orchestrate_plan = 'dynamic'; enabled_capabilities_set = $true; enabled_resources = @('docgen', 'search') }
$upd = Invoke-Api -Method PATCH -Uri "/v1/agent/sessions/$sid" -Body @{ config = $cfg } -Auth $authH
$backCfg = $upd.session.config
if ($backCfg.orchestrate_plan -ne 'dynamic') { Fail "orchestrate_plan 未生效: $($backCfg | ConvertTo-Json -Compress)" }
Write-Host "[OK] session=$sid orchestrate_plan=$($backCfg.orchestrate_plan) mode=$($backCfg.mode)" -ForegroundColor Green

Write-Host '=== 3/5 SSE 流式编排对话（动态分解）===' -ForegroundColor Cyan
$goal = '请围绕「递归与分治」设计一份适合大二学生的教学单元'
$sseResp = Invoke-WebRequest -Method POST -Uri "$base/v1/agent/sessions/$sid/chat/stream" `
    -Headers ($authH + @{ 'X-Request-Id' = "orch-$rand" }) `
    -ContentType 'application/json; charset=utf-8' `
    -Body (@{ content = $goal } | ConvertTo-Json -Compress) -UseBasicParsing
$sse = $sseResp.Content

# 解析 SSE：统计 task_status 事件、delta 与最终 done。
# 注意：gateway 序列化的 JSON 字段间无空格（如 "task_type":"task_started"）。
$taskStarted = @($sse | Select-String 'task_type":"task_started').Count
$taskFinished = @($sse | Select-String 'task_type":"task_finished').Count
$hasDone = $sse -match 'event: done'
$hasError = $sse -match 'event: error'
Write-Host "  task_started=$taskStarted task_finished=$taskFinished done=$hasDone error=$hasError" -ForegroundColor DarkGray
if ($taskStarted -lt 1) { Fail "未收到 task_started 进度事件（动态分解未执行或事件缺失）" }
if ($taskFinished -lt 1) { Fail "未收到 task_finished 进度事件" }
if ($hasError) { Fail "SSE 流中出现了 error 事件`n$sse" }
if (-not $hasDone) { Fail "SSE 未以 done 结束`n$sse" }

# 输出编排最终回答前 300 字符
$final = ($sse -split 'event: done')[0]
$final = ($final -split "data: " | Where-Object { $_ -match '"type":"delta"' } | ForEach-Object {
    if ($_ -match '"content":"(.*?)","') { $matches[1] }
}) -join ''
Write-Host "[OK] 动态编排完成，最终回答摘要: $($final.Substring(0, [Math]::Min($final.Length, 200)))" -ForegroundColor Green

Write-Host '=== 4/5 编排过程入库检查（orchestration_runs）===' -ForegroundColor Cyan
$msgs = Invoke-Api -Method GET -Uri "/v1/agent/sessions/$sid/messages" -Auth $authH
$orchMsg = @($msgs.messages | Where-Object { $_.content -like '__orch_v1__*' }).Count
if ($orchMsg -lt 1) { Fail '历史消息未包含 __orch_v1__ 编排过程摘要' }
Write-Host "[OK] 历史含 $orchMsg 条编排过程摘要（system，供历史渲染）" -ForegroundColor Green

Write-Host '=== 5/5 清理 ===' -ForegroundColor Cyan
Invoke-Api -Method POST -Uri '/v1/auth/logout' -Body @{ refresh_token = $login.refresh_token } -Auth $authH | Out-Null
Write-Host "`n🎉 动态编排端到端验证通过（user=$username, session=$sid, request_id=orch-$rand）" -ForegroundColor Green
