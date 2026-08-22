# 一键构建 + 安装安卓 debug 包到真机（免手动复制 APK）。
#
# 用法：
#   .\scripts\android-install.ps1               # 构建 aarch64 debug 并 adb 安装到已连真机
#   .\scripts\android-install.ps1 -SkipBuild    # 跳过构建，直接用上次的 debug APK 重装
#   .\scripts\android-install.ps1 -CleanInstall # 卸载旧包再装（清数据，保留 vs 覆盖二选一）
#
# 前置：Android SDK 在 D:\AndroidStudioSdk，真机 USB 调试已连（adb devices 可见）。
# 产物：D:\Agent\desktop\src-tauri\gen\android\app\build\outputs\apk\universal\debug\app-universal-debug.apk

param(
    # 跳过本地 gradle 构建，直接用现有 debug APK（改前端后需先构建）。
    [switch]$SkipBuild,
    # 先卸载旧包再安装（清数据）。默认覆盖安装保留本地登录态。
    [switch]$CleanInstall,
    # 指定手机 adb 序列号（多设备时）；留空用唯一已连设备。
    [string]$Serial = ''
)

$ErrorActionPreference = 'Stop'

$repoRoot   = Join-Path $PSScriptRoot '..'
$sdk        = 'D:\AndroidStudioSdk'
$ndk        = "$sdk\ndk\30.0.15729638"
$adb        = "$sdk\platform-tools\adb.exe"
$apkDir     = Join-Path $repoRoot 'desktop\src-tauri\gen\android\app\build\outputs\apk\universal\debug'
$apk        = Join-Path $apkDir 'app-universal-debug.apk'
$package    = 'com.nebula.agent'

# 定位设备
function Get-Device {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'   # adb 启动 daemon 会写 stderr，避免被当作致命错误
    try {
        $raw = & $adb devices 2>&1
    } finally { $ErrorActionPreference = $prev }
    $lines = @($raw | ForEach-Object { "$_" }) | Where-Object { $_ -match '^\S+\s+device$' }
    $ids = @($lines | ForEach-Object { ($_ -split '\s+')[0] })
    if ($Serial) {
        if ($ids -notcontains $Serial) { throw "未找到指定设备 $Serial（已连设备：$($ids -join ', ')）" }
        return $Serial
    }
    if ($ids.Count -eq 0) { throw '未检测到已连接的设备，请开启手机 USB 调试并连接' }
    if ($ids.Count -gt 1) { throw "检测到多台设备（$($ids -join ', ')），请用 -Serial 指定" }
    return $ids[0]
}

# 构建（可选）
if (-not $SkipBuild) {
    Write-Host '[1/3] 构建 aarch64 debug 包...' -ForegroundColor Cyan
    $env:ANDROID_HOME = $sdk
    $env:ANDROID_SDK_ROOT = $sdk
    $env:ANDROID_NDK_HOME = $ndk
    Push-Location (Join-Path $repoRoot 'desktop')
    try {
        & npx tauri android build --target aarch64 --debug
        if ($LASTEXITCODE -ne 0) { throw 'tauri android build 失败' }
    } finally { Pop-Location }
}
else {
    Write-Host '[1/3] 跳过构建，复用现有 debug APK' -ForegroundColor Cyan
}

if (-not (Test-Path $apk)) { throw "未找到 APK：$apk（先跑完整构建）" }

$device = Get-Device
Write-Host "[2/3] 目标设备: $device" -ForegroundColor Cyan

if ($CleanInstall) {
    Write-Host "  卸载旧包 $package..."
    & $adb -s $device uninstall $package 2>$null | Out-Null
}

Write-Host "[3/3] 安装 $apk ..."
& $adb -s $device install -r "$apk"
if ($LASTEXITCODE -ne 0) { throw 'adb install 失败' }

Write-Host ''
Write-Host '[完成] 安装成功，已自动安装到手机。' -ForegroundColor Green
Write-Host "  包名: $package"
Write-Host '  若需 Chrome DevTools 调试：电脑 Chrome 打开 chrome://inspect 选设备即可（需已连接 + debug 包）。'