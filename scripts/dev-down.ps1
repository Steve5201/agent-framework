# 停止并清理全栈（P2-62）。
# 用法：.\scripts\dev-down.ps1 [-Volumes]（-Volumes 同时删除数据卷，慎用）
param(
    [switch]$Volumes
)

$ErrorActionPreference = 'Stop'
$repoRoot = Join-Path $PSScriptRoot '..'
$composeFile = Join-Path $repoRoot 'deploy\docker-compose.yml'
$devComposeFile = Join-Path $repoRoot 'deploy\docker-compose.dev.yml'

$args = @('compose', '-f', $composeFile, '-f', $devComposeFile, '--profile', 'dev', 'down')
if ($Volumes) { $args += '-v' }

Push-Location $repoRoot
try {
    & docker @args
    if ($LASTEXITCODE -ne 0) { throw "docker compose down 失败，退出码 $LASTEXITCODE" }
}
finally { Pop-Location }

Write-Host "[OK] 全栈已停止" -ForegroundColor Green
