# 数据库迁移脚本：对指定数据库执行 up / down 迁移。
#
# 前置条件：
#   - 已设置环境变量 DB_PASSWORD（数据库密码）
#   - 已安装 migrate CLI（首次需执行）：
#     go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest
#
# 用法：
#   .\scripts\migrate.ps1 -Database auth          # 执行 up
#   .\scripts\migrate.ps1 -Database auth -Down    # 回滚最近 1 个版本
param(
    [Parameter(Mandatory = $true)]
    [string]$Database,

    [switch]$Down
)

$ErrorActionPreference = 'Stop'

$dbPassword = $env:DB_PASSWORD
if (-not $dbPassword) {
    throw '请先设置环境变量 DB_PASSWORD（如: $env:DB_PASSWORD="你的密码"）'
}

$migrate = Join-Path $env:GOPATH 'bin\migrate.exe'
if (-not (Test-Path $migrate)) {
    throw "未找到 migrate CLI: $migrate，请先执行: go install -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest"
}

$url = "postgres://postgres:$dbPassword@localhost:5432/$Database?sslmode=disable"
# 注意：migrate CLI 会把 Windows 反斜杠路径解析为 URL（报 invalid character），
# 因此迁移路径必须用正斜杠。
$path = "migrations/$Database"

Push-Location (Join-Path $PSScriptRoot '..\backend')
try {
    if ($Down) {
        & $migrate -path $path -database $url down 1
    }
    else {
        & $migrate -path $path -database $url up
    }
    if ($LASTEXITCODE -ne 0) { throw "migrate 执行失败，退出码 $LASTEXITCODE" }
    Write-Host "[OK] Database $Database migration done (path: $path)" -ForegroundColor Green
}
finally {
    Pop-Location
}
