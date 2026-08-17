# 密码重置脚本（万不得已时使用，如遗忘 / 泄露后应急更换）。
#
# 原理：直接修改 auth 库 users.password_hash（bcrypt，不可逆向还原，只能重置）。
# 后端 auth 服务本身没有"管理员改密"接口，故提供本工具。
# 强度规则与业务层一致：新密码 ≥8 位且同时包含字母与数字。
#
# 数据库访问：通过 docker compose exec 进入 postgres 容器执行 psql
# （本机 5432 端口可能被本机原生 postgres 占用，故不依赖宿主端口直连）。
#
# 用法（仓库根目录 PowerShell）：
#   .\scripts\reset-password.ps1 -List                        # 列出所有用户名
#   .\scripts\reset-password.ps1 -Username admin               # 交互输入新密码（终端无回显）
#   .\scripts\reset-password.ps1 -Username admin -Generate     # 生成随机强密码并打印一次
#   .\scripts\reset-password.ps1 -Username math_admin -Password 'Math@2026'
#
# 前置：postgres 容器已启动（docker compose ps 可见 Up）。
param(
    [string]$Username,   # 要重置的用户名（与 -List 互斥）
    [string]$Password,   # 新密码（省略则交互输入；与 -Generate 互斥）
    [switch]$Generate,   # 生成随机强密码（仅打印一次，请立即保存）
    [switch]$List        # 只读：列出全部用户名，不修改任何数据
)

$ErrorActionPreference = 'Stop'
$repoRoot = Join-Path $PSScriptRoot '..'
$compose  = Join-Path $repoRoot 'deploy\docker-compose.yml'
$backend  = Join-Path $repoRoot 'backend'

if (-not (Test-Path $compose)) {
    Write-Host "[错误] 未找到 deploy/docker-compose.yml" -ForegroundColor Red
    exit 1
}

# psql 统一入口：进 postgres 容器（本地 socket trust，无需密码）。
function Invoke-Psql {
    param([string]$Sql)
    docker compose -f $compose exec -T postgres psql -U postgres -d auth -t -A -c $Sql
    if ($LASTEXITCODE -ne 0) { throw "psql 执行失败（退出码 $LASTEXITCODE）" }
}

# 1. -List 只读模式。
if ($List) {
    if ($Username -or $Password -or $Generate) {
        Write-Host "[错误] -List 为只读模式，不能与 -Username/-Password/-Generate 同时使用" -ForegroundColor Red
        exit 2
    }
    Write-Host "用户名列表："
    Invoke-Psql "SELECT username FROM users ORDER BY id"
    exit 0
}

if (-not $Username) {
    Write-Host "[错误] 必须指定 -Username（忘记有哪些用户可用 -List 查看）" -ForegroundColor Red
    exit 2
}
if ($Generate -and $Password) {
    Write-Host "[错误] -Generate 与 -Password 互斥，只能二选一" -ForegroundColor Red
    exit 2
}

# 2. 确认操作（重置立即生效，旧密码作废；正在登录的会话不受影响）。
Write-Host "即将重置用户「$Username」的密码。" -ForegroundColor Yellow
$confirm = Read-Host "确认继续？输入该用户名以确认"
if ($confirm -ne $Username) {
    Write-Host "[已取消] 输入不一致，未做任何修改" -ForegroundColor Yellow
    exit 0
}

# 3. 计算 bcrypt 哈希（-hash-only 不连数据库；stdout 仅输出哈希，供 psql 注入）。
$passArgs = @('run', './cmd/passwd', '-hash-only')
if ($Generate) {
    $passArgs += '-generate'
} elseif ($Password) {
    $passArgs += '-password', $Password
}
Push-Location $backend
try {
    $hash = (& go @passArgs 2>&1 | Where-Object { $_ -is [string] -and $_.Trim() -match '^\$2[aby]\$' } | Select-Object -Last 1).Trim()
    if ($LASTEXITCODE -ne 0) {
        # 校验类错误（如强度不满足）会走 stderr；未产生哈希时同样视为失败
        if (-not $hash) { throw "无法生成密码哈希（请检查新密码是否满足 ≥8 位且含字母与数字）" }
    }
}
finally { Pop-Location }

if (-not $hash) {
    Write-Host "[错误] 无法生成密码哈希" -ForegroundColor Red
    exit 1
}

# 4. 注入哈希到 auth 库。
# 注意：psql -c 不执行变量替换，故用 stdin 管道 + 直接拼接（用户名限字母数字下划线，无注入风险）。
$sql = "UPDATE users SET password_hash = '$hash', updated_at = now() WHERE username = '$Username' RETURNING username;"
$result = $sql | docker compose -f $compose exec -T postgres psql -U postgres -d auth -t -A
if ($LASTEXITCODE -ne 0) { throw "psql 执行失败（退出码 $LASTEXITCODE）" }
if (-not $result) {
    Write-Host "[错误] 用户「$Username」不存在（可用 -List 查看全部用户名）" -ForegroundColor Red
    exit 1
}

Write-Host "[OK] 用户「$Username」密码已重置，可用新密码登录。" -ForegroundColor Green
if ($Generate) {
    Write-Host "[注意] 若未在终端看到随机密码，请重新执行并留意上方输出。" -ForegroundColor Yellow
}
