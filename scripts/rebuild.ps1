# rebuild.ps1 —— 一键重新编译 + 重启全部服务（2026-08-07）。
#
# 用途：改完代码后跑一次，自动完成：
#   ① 后端编译检查（framework + backend，go build + go vet，快）
#   ② 前端编译检查（web，tsc -b + vite build）
#   ③ 桌面端编译检查（desktop，cargo check，可用 -SkipDesktopCheck 跳过）
#   ④ （可选 -Test）跑后端 + 前端单元测试
#   ⑤ 重建并重启后端全栈（docker compose up -d --build：PG + auth + llm-gateway + sandbox + agent + gateway）
#   ⑥ 启动 web dev server（:3000，新窗口；已在运行则复用——vite 热更新自动应用改动）
#   ⑦ （可选 -Desktop）启动桌面端（tauri dev，:3001，新窗口）
#
# 常用用法：
#   .\scripts\rebuild.ps1                            # 全部编译 + 重启后端 + 起 web
#   .\scripts\rebuild.ps1 -Test                      # 额外跑单元测试
#   .\scripts\rebuild.ps1 -Desktop                   # 编译后顺带启动桌面端
#   .\scripts\rebuild.ps1 -BuildOnly                 # 只编译不重启/不启动
#   .\scripts\rebuild.ps1 -SkipDesktopCheck          # 跳过较慢的 cargo check
#   .\scripts\rebuild.ps1 -ForceRebuild              # 强制 --no-cache 全量重建镜像
#                                                    # （正常改代码不需要；仅缓存异常时用）
#
# 说明：docker compose build 的 COPY 层按源码内容失效，改了 Go 代码正常就会重编；
#       本脚本在 build 后按服务校验镜像 Created 时间戳，确认新源码确实进镜像，
#       未重建会直接报错并提示 --no-cache，避免"假成功"误判。
#
# 前置：Docker Desktop 已启动；deploy/.env 已配置（DB_PASSWORD / DEEPSEEK_API_KEY / JWT_SECRET）。
param(
    [switch]$Test,               # 编译检查后运行后端 go test + web vitest
    [switch]$Desktop,            # 同时启动桌面端（tauri dev，:3001，独立新窗口）
    [switch]$BuildOnly,          # 只做编译检查，不重启/不启动任何服务
    [switch]$SkipWeb,            # 跳过 web 编译检查与 dev server
    [switch]$SkipDesktopCheck,   # 跳过桌面端 cargo check（较慢）
    [switch]$ForceRebuild        # docker compose build 传 --no-cache（缓存异常/依赖版本变动时的兜底）
)

$ErrorActionPreference = 'Stop'
$repoRoot   = Join-Path $PSScriptRoot '..'
$composeFile = Join-Path $repoRoot 'deploy\docker-compose.yml'
$devComposeFile = Join-Path $repoRoot 'deploy\docker-compose.dev.yml'
$envFile     = Join-Path $repoRoot 'deploy\.env'
$webDir      = Join-Path $repoRoot 'web'
$desktopDir  = Join-Path $repoRoot 'desktop'
$fwDir       = Join-Path $repoRoot 'framework'
$beDir       = Join-Path $repoRoot 'backend'
$sw          = [System.Diagnostics.Stopwatch]::StartNew()

function Write-Step([int]$n, [string]$msg) { Write-Host "`n[$n] $msg" -ForegroundColor Cyan }
function Write-OK([string]$msg)  { Write-Host "  ✔ $msg" -ForegroundColor Green }
function Write-Info([string]$msg) { Write-Host "  · $msg" -ForegroundColor DarkGray }

# 原生命令执行封装：PS 5.1 下 $ErrorActionPreference='Stop' 会把原生 stderr
# （如 vite 的 chunk 体积警告、go 的正常输出）误判为终止错误，导致"构建已成功
# 却被脚本当失败中止"。统一在原生调用期间降为 Continue，只以退出码判定成败；
# 失败时抛出清晰信息（含退出码）。
function Invoke-Native([string]$FailMsg, [scriptblock]$Cmd) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $Cmd } finally { $ErrorActionPreference = $prev }
    if ($LASTEXITCODE -ne 0) { throw "$FailMsg（退出码 $LASTEXITCODE）" }
}

# ---------------------------------------------------------------------------
# 0. 前置校验
# ---------------------------------------------------------------------------
Write-Step 0 "前置校验"

if (-not $BuildOnly) {
    if (Get-Command docker -ErrorAction SilentlyContinue) {
        Write-OK "docker 已安装"
    } else {
        Write-Host "[错误] 未找到 docker 命令，请先安装并启动 Docker Desktop" -ForegroundColor Red
        exit 1
    }
    if (-not (Test-Path $envFile)) {
        Write-Host "[错误] 未找到 deploy/.env。请先执行：" -ForegroundColor Red
        Write-Host "       Copy-Item deploy\.env.example deploy\.env" -ForegroundColor Yellow
        Write-Host "       并填写 DB_PASSWORD / DEEPSEEK_API_KEY / JWT_SECRET" -ForegroundColor Yellow
        exit 1
    }
    foreach ($key in 'DB_PASSWORD', 'DEEPSEEK_API_KEY', 'JWT_SECRET') {
        $line = Select-String -Path $envFile -Pattern "^$key=" | Select-Object -First 1
        if (-not $line -or $line.Line -match "=$") {
            Write-Host "[错误] .env 中未配置 $key，请补齐后重试" -ForegroundColor Red
            exit 1
        }
    }
    Write-OK ".env 必填项齐全"
}

# ---------------------------------------------------------------------------
# 1. 后端编译检查（framework + backend）
# ---------------------------------------------------------------------------
Write-Step 1 "后端编译检查（go build + go vet）"

function Invoke-GoCheck([string]$dir, [string]$label) {
    Push-Location $dir
    try {
        Invoke-Native "$label go build 失败" { & go build ./... }
        Invoke-Native "$label go vet 失败" { & go vet ./... }
        Write-OK "$label build + vet 通过"
    }
    finally { Pop-Location }
}

Invoke-GoCheck $fwDir 'framework'
Invoke-GoCheck $beDir 'backend'

# ---------------------------------------------------------------------------
# 2. web 编译检查（tsc -b + vite build）
# ---------------------------------------------------------------------------
if (-not $SkipWeb) {
    Write-Step 2 "前端编译检查（tsc + vite build）"
    Push-Location $webDir
    try {
        Invoke-Native "web 构建失败" { & npm run build }
        Write-OK "web 构建通过"
    }
    finally { Pop-Location }
} else {
    Write-Step 2 "前端编译检查（已跳过 -SkipWeb）"
}

# ---------------------------------------------------------------------------
# 3. 桌面端编译检查（cargo check）
# ---------------------------------------------------------------------------
if (-not $SkipDesktopCheck) {
    Write-Step 3 "桌面端编译检查（cargo check，可 -SkipDesktopCheck 跳过）"
    Invoke-Native "desktop cargo check 失败" { & cargo check --manifest-path (Join-Path $desktopDir 'src-tauri\Cargo.toml') }
    Write-OK "desktop cargo check 通过"
} else {
    Write-Step 3 "桌面端编译检查（已跳过 -SkipDesktopCheck）"
}

# ---------------------------------------------------------------------------
# 4.（可选）单元测试
# ---------------------------------------------------------------------------
if ($Test) {
    Write-Step 4 "单元测试"
    Push-Location $fwDir
    try { Invoke-Native 'framework 测试失败' { & go test ./... -count=1 } }
    finally { Pop-Location }
    Push-Location $beDir
    try { Invoke-Native 'backend 测试失败' { & go test ./... -count=1 } }
    finally { Pop-Location }
    if (-not $SkipWeb) {
        Push-Location $webDir
        try { Invoke-Native 'web 测试失败' { & npm run test } }
        finally { Pop-Location }
    }
    Write-OK "全部单元测试通过"
}

if ($BuildOnly) {
    $sw.Stop()
    Write-Host "`n[完成] 编译检查全部通过，耗时 $($sw.Elapsed.TotalSeconds.ToString('0.0'))s（-BuildOnly 未启动任何服务）" -ForegroundColor Green
    exit 0
}

# ---------------------------------------------------------------------------
# 5. 重建 + 重启后端全栈（docker compose）
# ---------------------------------------------------------------------------
Write-Step 5 "重建并重启后端全栈（docker compose build + up -d）"
$svcs = 'auth', 'llm-gateway', 'sandbox', 'agent', 'rag', 'gateway'
# 记录构建前各服务镜像 ID，用于构建后核对哪些服务真正重建了。
# 用 docker images -q 查询：镜像不存在时输出空串（不报错），支持首次全新构建。
$beforeIds = @{}
foreach ($svc in $svcs) {
    $beforeIds[$svc] = ((& docker images -q --filter "reference=agent-stack-$svc" | Select-Object -First 1) 2>$null).Trim()
}
Push-Location $repoRoot
try {
    if ($ForceRebuild) {
        Write-Info "强制全量重建（--no-cache，需重新拉取全部依赖，较慢）"
        Invoke-Native "docker compose build 失败（--no-cache）" { & docker compose -f $composeFile -f $devComposeFile build --no-cache }
    } else {
        Invoke-Native "docker compose build 失败" { & docker compose -f $composeFile -f $devComposeFile build }
    }
    # P6-B：dev 覆盖 + dev profile（拉起本地 ollama），与 dev-up.ps1 一致。
    Invoke-Native "docker compose up 失败" { & docker compose -f $composeFile -f $devComposeFile --profile dev up -d }
}
finally { Pop-Location }

# 自检：对比构建前后镜像 ID，确认改动对应服务确实重建。
# 全部未变 = 源码没进镜像（BuildKit 缓存异常），直接报错，防"假成功"。
$rebuilt = @()
foreach ($svc in $svcs) {
    $nowId = ((& docker images -q --filter "reference=agent-stack-$svc" | Select-Object -First 1) 2>$null).Trim()
    if ($nowId -and $nowId -ne $beforeIds[$svc]) { $rebuilt += $svc }
}
if ($rebuilt.Count -eq 0) {
    Write-Host "[失败] 没有任何服务镜像被重建，新源码未进入镜像（疑似 BuildKit 缓存异常）。" -ForegroundColor Red
    Write-Host "  请强制全量重建后重试：docker compose -f $composeFile -f $devComposeFile build --no-cache" -ForegroundColor Yellow
    exit 1
}
Write-OK "本次重建了以下服务：$($rebuilt -join ', ')（新源码已进入镜像）"

# 等待 gateway 健康（最多 90s）。
Write-Host "  · 等待 gateway 健康…" -ForegroundColor DarkGray
$base = 'http://localhost:8080'
$deadline = (Get-Date).AddSeconds(90)
$ready = $false
while ((Get-Date) -lt $deadline) {
    try {
        $resp = Invoke-WebRequest "$base/healthz" -UseBasicParsing -TimeoutSec 3
        if ($resp.StatusCode -eq 200) { $ready = $true; break }
    } catch { }
    Start-Sleep -Seconds 5
}
if (-not $ready) {
    Write-Host "[失败] 90s 内 gateway 未就绪，请查看日志：docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.dev.yml logs -f" -ForegroundColor Red
    exit 1
}
Write-OK "后端全栈已就绪（gateway $base/healthz → 200）"

# ---------------------------------------------------------------------------
# 6. 启动 web dev server（:3000）
# ---------------------------------------------------------------------------
function Test-Port([int]$port) {
    $tcp = New-Object System.Net.Sockets.TcpClient
    try { $tcp.Connect('127.0.0.1', $port); return $true }
    catch { return $false }
    finally { $tcp.Dispose() }
}

if (-not $SkipWeb) {
    Write-Step 6 "启动 web dev server"
    if (Test-Port 3000) {
        Write-Info " :3000 已在运行（vite 热更新会自动应用代码改动，无需重启）"
        Write-OK "web 开发服务器 http://localhost:3000"
    } else {
        Start-Process powershell -WorkingDirectory $webDir -ArgumentList '-NoExit', '-Command', 'npm run dev' | Out-Null
        Write-OK "已在独立窗口启动 vite → http://localhost:3000"
    }
}

# ---------------------------------------------------------------------------
# 7.（可选）启动桌面端（tauri dev，:3001）
# ---------------------------------------------------------------------------
if ($Desktop) {
    Write-Step 7 "启动桌面端（tauri dev）"
    Start-Process powershell -WorkingDirectory $desktopDir -ArgumentList '-NoExit', '-Command', 'npm run tauri dev' | Out-Null
    Write-OK "桌面端构建+启动中（独立窗口；devUrl http://localhost:3001）"
}

# ---------------------------------------------------------------------------
# 汇总
# ---------------------------------------------------------------------------
$sw.Stop()
Write-Host "`n========== 一键重建完成（$($sw.Elapsed.TotalSeconds.ToString('0.0'))s）==========" -ForegroundColor Green
Write-Host "  后端入口 : http://localhost:8080  （接口文档 /swagger/ui）" -ForegroundColor Cyan
if (-not $SkipWeb) { Write-Host "  Web 前端 : http://localhost:3000" -ForegroundColor Cyan }
if ($Desktop)    { Write-Host "  桌面端   : 独立窗口（:3001）" -ForegroundColor Cyan }
Write-Host "  管理端   : 浏览器登录 admin/Admin@2026 后进入" -ForegroundColor Cyan
Write-Host "  查看日志 : docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.dev.yml logs -f" -ForegroundColor Cyan
Write-Host "  冒烟测试 : .\scripts\smoke.ps1" -ForegroundColor Cyan
Write-Host "  停止全栈 : .\scripts\dev-down.ps1" -ForegroundColor Cyan
