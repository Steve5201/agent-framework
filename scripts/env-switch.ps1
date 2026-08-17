# 环境配置快速切换（P6-A）。
#
# 本地/云端两套环境变量配置，一键切换：
#   .\scripts\env-switch.ps1 -Target dev     # 本地开发（localhost + 本地 Ollama）
#   .\scripts\env-switch.ps1 -Target prod    # 云端生产（公网 IP + 硅基流动）
#
# 作用：把 deploy/.env.dev 或 deploy/.env.prod 复制为 deploy/.env（compose 实际读取），
# 并打印生效的差异值摘要，便于核对切对了。
# 注意：修改后需重建容器才生效：.\scripts\dev-up.ps1 -Rebuild
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('dev', 'prod')]
    [string]$Target
)

$ErrorActionPreference = 'Stop'

$repoRoot = Join-Path $PSScriptRoot '..'
$srcFile = Join-Path $repoRoot "deploy\.env.$Target"
$envFile = Join-Path $repoRoot 'deploy\.env'

# 1. 校验源文件存在。
if (-not (Test-Path $srcFile)) {
    Write-Host "[错误] 未找到 $srcFile，请确认 P6-A 已交付该文件" -ForegroundColor Red
    exit 1
}

# 2. 复制为 .env。
Copy-Item -Path $srcFile -Destination $envFile -Force
Write-Host "[OK] 已切换为 [$Target] 环境：$srcFile -> deploy\.env" -ForegroundColor Green

# 3. 读取 .env 键值（跳过注释/空行/含空格的键），供摘要与必填校验。
# 注意 -Encoding UTF8：.env 是 UTF-8 无 BOM，PS5.1 默认按 GBK 误读中文注释会吞换行。
$kv = @{}
foreach ($line in Get-Content $envFile -Encoding UTF8) {
    if ($line -match '^([A-Za-z_][A-Za-z0-9_]*)=(.*)$') {
        $kv[$matches[1]] = $matches[2]
    }
}

# 4. 必填校验（prod 提示部署前必须补齐；dev 必须已填）。
$required = @('DB_PASSWORD', 'DEEPSEEK_API_KEY', 'JWT_SECRET')
foreach ($key in $required) {
    if (-not $kv.ContainsKey($key) -or [string]::IsNullOrWhiteSpace($kv[$key])) {
        if ($Target -eq 'prod') {
            Write-Host "[警告] prod 环境 $key 未配置，部署前必须补齐（生成：openssl rand -hex 16）" -ForegroundColor Yellow
        }
        else {
            Write-Host "[错误] dev 环境缺少必填项 $key，请补齐 deploy\.env.dev 后重试" -ForegroundColor Red
            exit 1
        }
    }
}

# 5. 差异值摘要（P6-A 五个差异点），切换后一眼确认。
Write-Host ""
Write-Host "当前生效差异值（[$Target]）:" -ForegroundColor Cyan
function Show-Diff([string]$Label, [string]$Key) {
    $v = $kv[$Key]
    if ([string]::IsNullOrWhiteSpace($v)) { $v = '<未设置，走默认>' }
    Write-Host ("  {0,-24} {1}" -f $Label, $v) -ForegroundColor White
}
Show-Diff 'GATEWAY_CORS_ORIGINS' 'GATEWAY_CORS_ORIGINS'
Show-Diff 'AGENT_FILES_BASE_URL' 'AGENT_FILES_BASE_URL'
Show-Diff 'RAG_EMBEDDING_BASE_URL' 'RAG_EMBEDDING_BASE_URL'
Show-Diff 'RAG_EMBEDDING_MODEL' 'RAG_EMBEDDING_MODEL'
Show-Diff 'AUTH_ADMIN_USERNAME' 'AUTH_ADMIN_USERNAME'
Show-Diff 'AUTH_ADMIN_PASSWORD' 'AUTH_ADMIN_PASSWORD'

# ADMIN_MODELS 摘要：解析 JSON 只列模型名，避免打印超长行/密钥。
if ($kv.ContainsKey('ADMIN_MODELS') -and -not [string]::IsNullOrWhiteSpace($kv['ADMIN_MODELS'])) {
    try {
        $models = $kv['ADMIN_MODELS'] | ConvertFrom-Json
        $names = ($models | ForEach-Object { $_.name }) -join ', '
        Write-Host ("  {0,-24} {1}" -f 'ADMIN_MODELS', $names) -ForegroundColor White
    }
    catch {
        Write-Host "  ADMIN_MODELS                 <JSON 解析失败，请检查格式>" -ForegroundColor Yellow
    }
}
else {
    Write-Host "  ADMIN_MODELS                 <未配置，管理端手动添加模型>" -ForegroundColor White
}

Write-Host ""
Write-Host "提示：切换到新环境后需重建容器生效：.\scripts\dev-up.ps1 -Rebuild" -ForegroundColor Cyan
