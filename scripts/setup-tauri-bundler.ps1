# setup-tauri-bundler.ps1 —— 一键安装 Tauri 打包工具（NSIS / WiX）到用户缓存目录（P2-87 补充）。
#
# 背景：tauri build 打 MSI/NSIS 需要 WiX / NSIS 工具，默认从 GitHub 实时下载，
#       国内网络常超时（timeout: global）。本脚本改为从项目 .tools/tauri-tools/
#       读取已归档的工具包，解压到 %LOCALAPPDATA%\tauri\（Tauri 缓存目录），
#       之后打包直接复用，不再联网下载。
#
# 用法：
#   1. 一次性归档（用浏览器下载 3 个文件到 .tools\tauri-tools\）：
#        nsis-3.11.zip             github.com/tauri-apps/binary-releases .../nsis-3.11/nsis-3.11.zip
#        nsis_tauri_utils.dll      github.com/tauri-apps/nsis-tauri-utils .../nsis_tauri_utils-v0.5.3/nsis_tauri_utils.dll
#        wix314-binaries.zip       github.com/wixtoolset/wix3 .../wix3141rtm/wix314-binaries.zip
#   2. 运行本脚本（幂等：缓存已就绪则直接跳过）。
#   3. 照常执行 npm run build（desktop 下）。
#
# ⚠ 版本匹配：nsis_tauri_utils.dll 必须与 tauri CLI 期望的版本一致，否则打包时报
#   "NSIS directory contains mis-hashed files. Redownloading them."（会再走 GitHub 下载）。
#   当前 @tauri-apps/cli 2.11.x 期望 v0.5.3；升级 CLI 后若报哈希错误，先在此改版本号重新归档。
param()

$ErrorActionPreference = 'Stop'

$srcDir = Join-Path $PSScriptRoot '..\.tools\tauri-tools'
$cache  = Join-Path $env:LOCALAPPDATA 'tauri'

Write-Host "==> 工具归档目录: $srcDir" -ForegroundColor Cyan

# ---- 1. 检查归档文件是否齐全 ----
$required = @('nsis-3.11.zip', 'nsis_tauri_utils.dll', 'wix314-binaries.zip')
$missing = @($required | Where-Object { -not (Test-Path (Join-Path $srcDir $_)) })
if ($missing.Count -gt 0) {
    Write-Host '[错误] .tools\tauri-tools\ 缺少以下文件（先用浏览器下载后放入）：' -ForegroundColor Red
    foreach ($f in $missing) { Write-Host "   - $f" -ForegroundColor Yellow }
    Write-Host '下载地址（GitHub）：' -ForegroundColor Yellow
    Write-Host '   nsis-3.11.zip:        https://github.com/tauri-apps/binary-releases/releases/download/nsis-3.11/nsis-3.11.zip'
    Write-Host '   nsis_tauri_utils.dll: https://github.com/tauri-apps/nsis-tauri-utils/releases/download/nsis_tauri_utils-v0.5.3/nsis_tauri_utils.dll'
    Write-Host '   wix314-binaries.zip:  https://github.com/wixtoolset/wix3/releases/download/wix3141rtm/wix314-binaries.zip'
    exit 1
}

# ---- 2. NSIS（目标: %LOCALAPPDATA%\tauri\NSIS\makensis.exe）----
$nsisDest = Join-Path $cache 'NSIS'
if (-not (Test-Path (Join-Path $nsisDest 'makensis.exe'))) {
    Write-Host '==> 安装 NSIS ...' -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path $nsisDest | Out-Null
    $tmp = Join-Path $cache 'nsis-tmp'
    Expand-Archive -Path (Join-Path $srcDir 'nsis-3.11.zip') -DestinationPath $tmp -Force
    $sub = Get-ChildItem $tmp
    $src = if ($sub.Count -eq 1 -and $sub[0].PSIsContainer) { $sub[0].FullName } else { $tmp }
    Get-ChildItem $src | Move-Item -Destination $nsisDest -Force
    Remove-Item $tmp -Recurse -Force
} else {
    Write-Host '==> NSIS 已就绪，跳过' -ForegroundColor DarkGray
}

# 无论是否已安装，都强制覆盖 nsis_tauri_utils.dll——
# 版本不匹配时 tauri 会报 "mis-hashed files" 并重新走 GitHub 下载（国内常超时）。
New-Item -ItemType Directory -Force -Path (Join-Path $nsisDest 'Plugins\x86-unicode\additional') | Out-Null
Copy-Item (Join-Path $srcDir 'nsis_tauri_utils.dll') (Join-Path $nsisDest 'Plugins\x86-unicode\nsis_tauri_utils.dll') -Force
Copy-Item (Join-Path $srcDir 'nsis_tauri_utils.dll') (Join-Path $nsisDest 'Plugins\x86-unicode\additional\nsis_tauri_utils.dll') -Force
Write-Host '==> nsis_tauri_utils.dll 已覆盖为归档版本' -ForegroundColor DarkGray

# ---- 3. WiX（目标: %LOCALAPPDATA%\tauri\WixTools314\light.exe）----
$wixDest = Join-Path $cache 'WixTools314'
if (-not (Test-Path (Join-Path $wixDest 'light.exe'))) {
    Write-Host '==> 安装 WiX ...' -ForegroundColor Cyan
    New-Item -ItemType Directory -Force -Path $wixDest | Out-Null
    $tmp = Join-Path $cache 'wix-tmp'
    Expand-Archive -Path (Join-Path $srcDir 'wix314-binaries.zip') -DestinationPath $tmp -Force
    $sub = Get-ChildItem $tmp
    $src = if ($sub.Count -eq 1 -and $sub[0].PSIsContainer) { $sub[0].FullName } else { $tmp }
    Get-ChildItem $src | Move-Item -Destination $wixDest -Force
    Remove-Item $tmp -Recurse -Force
} else {
    Write-Host '==> WiX 已就绪，跳过' -ForegroundColor DarkGray
}

# ---- 4. 自检 ----
$ok1 = Test-Path (Join-Path $nsisDest 'makensis.exe')
$ok2 = Test-Path (Join-Path $wixDest 'light.exe')
if ($ok1 -and $ok2) {
    Write-Host "[OK] NSIS/WiX 就绪，可以直接打包（cd desktop; npm run build）" -ForegroundColor Green
    exit 0
}
Write-Host "[错误] 自检未通过：NSIS=$ok1 WiX=$ok2" -ForegroundColor Red
exit 1
