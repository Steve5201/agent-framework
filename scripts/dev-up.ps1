# 一键启动后端（P2-62 / P6-B）。
#
# 只启动后端（docker compose）：PostgreSQL + 六服务（auth / llm-gateway / sandbox /
# agent / rag / gateway）+ 本地 Ollama（dev profile，P6-B）。
# web 与 desktop 是客户端（连接 gateway:8080），无需单独启动；
# 运行桌面端/浏览器前先执行本脚本即可。
#
# 前置：已安装 Docker Desktop 且已启动；已创建 deploy/.env（见 .env.example）。
# 用法：.\scripts\dev-up.ps1 [-Rebuild]
param(
    [switch]$Rebuild   # 强制重新构建镜像（源码变更后建议）
)

$ErrorActionPreference = 'Stop'
$repoRoot = Join-Path $PSScriptRoot '..'
$composeFile = Join-Path $repoRoot 'deploy\docker-compose.yml'
$devComposeFile = Join-Path $repoRoot 'deploy\docker-compose.dev.yml'
$envFile = Join-Path $repoRoot 'deploy\.env'

# 1. 校验 .env 存在且必填项已填。
if (-not (Test-Path $envFile)) {
    Write-Host "[错误] 未找到 deploy/.env。请先执行：" -ForegroundColor Red
    Write-Host "       Copy-Item deploy\.env.example deploy\.env" -ForegroundColor Yellow
    Write-Host "       然后填写 DB_PASSWORD / DEEPSEEK_API_KEY / JWT_SECRET" -ForegroundColor Yellow
    exit 1
}
foreach ($key in 'DB_PASSWORD', 'DEEPSEEK_API_KEY', 'JWT_SECRET') {
    # -Encoding UTF8：.env 为 UTF-8 无 BOM，PS5.1 默认按 GBK 误读中文注释会吞换行
    # 导致键值行被并进注释行而误判"未配置"。
    $line = Select-String -Path $envFile -Pattern "^$key=" -Encoding UTF8 | Select-Object -First 1
    if (-not $line -or $line.Line -match "=$") {
        Write-Host "[错误] .env 中未配置 $key，请补齐后重试" -ForegroundColor Red
        exit 1
    }
}

# 2. 启动（可选强制重建）。P6-B：生产基础编排 + dev 覆盖 + dev profile（拉起本地 ollama）。
$buildArgs = @('compose', '-f', $composeFile, '-f', $devComposeFile, '--profile', 'dev', 'up', '-d')
if ($Rebuild) { $buildArgs += '--build' }
Push-Location $repoRoot
try {
    & docker @buildArgs
    if ($LASTEXITCODE -ne 0) { throw "docker compose up 失败，退出码 $LASTEXITCODE" }
}
finally { Pop-Location }

# 3. 等待 gateway 健康（最多 60s，5s 间隔）。
$base = 'http://localhost:8080'
$deadline = (Get-Date).AddSeconds(60)
$ready = $false
while ((Get-Date) -lt $deadline) {
    try {
        $resp = Invoke-WebRequest "$base/healthz" -UseBasicParsing -TimeoutSec 3
        if ($resp.StatusCode -eq 200) { $ready = $true; break }
    } catch {
        # 服务未就绪，继续等待
    }
    Start-Sleep -Seconds 5
}
if (-not $ready) {
    Write-Host "[失败] 60s 内 gateway 未就绪，请查看日志：docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.dev.yml logs" -ForegroundColor Red
    exit 1
}

Write-Host "[OK] 后端已就绪" -ForegroundColor Green
Write-Host "  对外入口: $base" -ForegroundColor Cyan
Write-Host "  web/desktop 客户端直接连接即可，无需再启动前端服务" -ForegroundColor Cyan
Write-Host "  接口文档: $base/swagger/ui" -ForegroundColor Cyan
Write-Host "  查看日志: docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.dev.yml logs -f" -ForegroundColor Cyan
Write-Host "  冒烟测试: .\scripts\smoke.ps1" -ForegroundColor Cyan
