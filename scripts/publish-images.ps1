# 镜像发布链路（P6-C）：本地 build → docker save 单包 → gzip 压缩 → scp → 服务器 load。
#
# 适用场景：阿里云 99 计划 ECS（2 核 / 1.6Gi / 40G / 3Mbps 带宽），服务器无法快速
# 构建镜像，必须本地打镜像后压缩搬运。实际应用镜像总量约 2.9GB（sandbox 含
# chromium + python 解析器占大头），gzip 后约 1.5~2GB，3Mbps 带宽一次传输约 1~2 小时，
# 建议服务器空闲时段执行。
#
# 用法（在 Windows 开发机执行）：
#   .\scripts\publish-images.ps1                      # 全流程：build + save + gzip + scp + load
#   .\scripts\publish-images.ps1 -SkipBuild           # 跳过 build，用本地现有镜像打包
#   .\scripts\publish-images.ps1 -SkipLoad            # 只打包 + scp，不在服务器 load
#   .\scripts\publish-images.ps1 -IncludeWeb          # 顺带发布 web 前端（后端镜像 + 前端一起同步）
#   .\scripts\publish-images.ps1 -IncludeWeb -SkipWebBuild  # 发布 web 但跳过本地构建（用现有 web\dist）
#   .\scripts\publish-images.ps1 -RemoteHost root@IP  # 指定服务器
#
# 前置条件：
#   - 本地已 docker compose 能正常构建（dev 环境验证过）；
#   - 服务器已装 Docker（P6-D）+ 开放 ssh（root@47.108.207.37）；
#   - 服务器部署用「生产编排」（docker-compose.yml 单文件），ollama 不部署，
#     pgvector 基础镜像服务器直拉（见 -PullBase）。
#
# 发布后部署：
#   ssh root@IP "cd /opt/agent-stack && docker compose up -d"
#
# 替代方案（带宽大时可改用）：阿里云 ACR 个人版（registry.cn-hangzhou.aliyuncs.com），
# 同区 ECS 走内网拉取可绕过 3Mbps 瓶颈（需本地/服务器分别 docker login ACR 账号）。
# 本脚本走最朴素的 scp 链路，不依赖云账号。

param(
    # 服务器地址（scp/ssh 目标）。
    [string]$RemoteHost = 'root@47.108.207.37',
    # 服务器上镜像包存放目录。
    [string]$RemoteDir = '/opt/agent-stack/images',
    # 附加版本号（如 v0.1.0）；留空则只打 latest。版本号会同时打本地与 load 后的镜像。
    [string]$ImageVersion = '',
    # 跳过本地 build（直接用现有镜像打包，发布前镜像已在本地 build 过时用它）。
    [switch]$SkipBuild,
    # 跳过服务器 docker load 与基础镜像拉取（只完成打包 + 上传）。
    [switch]$SkipLoad,
    # 服务器上顺带拉取基础镜像（pgvector/pg16；不拉 ollama，生产不部署）。
    [switch]$PullBase,
    # 顺带发布 web 前端静态资源（nginx 托管 /opt/agent-stack/web，见 -IncludeWeb）。
    [switch]$IncludeWeb,
    # 发布 web 时跳过本地构建（直接用现有 web\dist，需已包含目标服务器 API 地址）。
    [switch]$SkipWebBuild
)

$ErrorActionPreference = 'Stop'

$repoRoot  = Join-Path $PSScriptRoot '..'
$composeFile = Join-Path $repoRoot 'deploy\docker-compose.yml'
$outDir    = Join-Path $repoRoot 'deploy\data\images'   # 已入 .gitignore，不进版本库
$appServices = @('auth', 'llm-gateway', 'sandbox', 'agent', 'rag', 'gateway')
$tarFile   = Join-Path $outDir 'agent-stack-images.tar'
$gzFile    = Join-Path $outDir 'agent-stack-images.tar.gz'
$tag = if ($ImageVersion) { $ImageVersion } else { 'latest' }
$webDir   = Join-Path $repoRoot 'web\dist'
$webTar   = Join-Path $outDir 'web-dist.tar'
$webGz    = Join-Path $outDir 'web-dist.tar.gz'

# 应用镜像默认命名（compose 项目 agent-stack + 服务名）。
function Image-Ref([string]$svc, [string]$versionTag) { "agent-stack-$svc`:$versionTag" }

function Step([string]$msg) { Write-Host "[$msg]" -ForegroundColor Cyan }

# ---- 0. 参数回显 ----
Step "镜像发布链路"
Write-Host "  目标服务器 : $RemoteHost"
Write-Host "  镜像版本   : $tag  ($($appServices.Count) 个应用镜像)"
if ($SkipBuild) { Write-Host "  跳过 build : 是（用本地现有镜像）" }
if ($SkipLoad)  { Write-Host "  跳过 load  : 是（只打包上传）" }
if ($IncludeWeb) { Write-Host "  发布 web   : 是（nginx 托管前端）" }

New-Item -ItemType Directory -Force -Path $outDir | Out-Null

# ---- 1. 本地 build（生产组合，只 build 应用服务）----
if (-not $SkipBuild) {
    Step "1/5 docker compose build（生产编排，6 个应用镜像）"
    & docker compose -f $composeFile build $appServices
    if ($LASTEXITCODE -ne 0) { throw 'docker compose build 失败' }
}
else {
    Step "1/5 跳过 build，使用本地现有镜像"
    foreach ($svc in $appServices) {
        $ref = Image-Ref $svc $tag
        $hit = docker images --format '{{.Repository}}:{{.Tag}}' | Select-String -Pattern "^$([regex]::Escape($ref))$"
        if (-not $hit) { throw "本地缺少镜像 $ref，请先去掉 -SkipBuild 重新 build" }
    }
}

# ---- 2. 可选版本 tag ----
if ($ImageVersion) {
    Step "2/5 打版本 tag : $ImageVersion"
    foreach ($svc in $appServices) {
        & docker tag "agent-stack-$svc`:latest" "agent-stack-$svc`:$ImageVersion"
        if ($LASTEXITCODE -ne 0) { throw "docker tag agent-stack-$svc 失败" }
    }
}
else {
    Step "2/5 使用 latest 标签（未指定版本号）"
}

# ---- 3. save + gzip（落盘 tar → 流式压缩，避免内存占用）----
Step "3/5 docker save + gzip 打包（总量约 2.9GB，压缩需几分钟）"
$refs = $appServices | ForEach-Object { Image-Ref $_ $tag }
& docker save @refs -o $tarFile
if ($LASTEXITCODE -ne 0) { throw 'docker save 失败' }
$sizeTar = (Get-Item $tarFile).Length

# 流式 gzip（Windows 无 gzip 命令，用 python 按块压缩，不占内存）。
$py = @'
import gzip, sys
fin = open(sys.argv[1], 'rb')
fout = gzip.open(sys.argv[2], 'wb')
while True:
    chunk = fin.read(1 << 20)
    if not chunk:
        break
    fout.write(chunk)
fout.close(); fin.close()
'@
$pyFile = Join-Path $outDir '_gz_tmp.py'
[System.IO.File]::WriteAllText($pyFile, $py, (New-Object System.Text.UTF8Encoding($false)))
& python $pyFile $tarFile $gzFile
if ($LASTEXITCODE -ne 0) { throw 'gzip 压缩失败' }
Remove-Item -LiteralPath $pyFile -Force   # 永久删除临时脚本（不进回收站）
Remove-Item -LiteralPath $tarFile -Force  # 删除中间 tar，只留压缩包

$sizeGz = (Get-Item $gzFile).Length
$ratio  = [math]::Round(100.0 * $sizeGz / $sizeTar, 1)
$minutes = [math]::Round($sizeGz / (0.375MB) / 60.0, 1)   # 3Mbps ≈ 375KB/s
Write-Host "  tar: $([math]::Round($sizeTar/1MB,1)) MB → gz: $([math]::Round($sizeGz/1MB,1)) MB（压缩率 $ratio%）；3Mbps 预计传输 $minutes 分钟" -ForegroundColor Yellow

# ---- 4. scp 上传 ----
Step "4/5 scp 上传（$($RemoteHost):$RemoteDir）"
& ssh $RemoteHost "mkdir -p $RemoteDir"
& scp $gzFile "${RemoteHost}:${RemoteDir}/"
if ($LASTEXITCODE -ne 0) { throw 'scp 上传失败' }
Write-Host "  已上传 $gzFile" -ForegroundColor Green

# ---- 5. 服务器 load + 基础镜像 ----
if (-not $SkipLoad) {
    Step "5/5 服务器 docker load + 拉取基础镜像"
    & ssh $RemoteHost "docker load -i ${RemoteDir}/agent-stack-images.tar.gz"
    if ($LASTEXITCODE -ne 0) { throw '服务器 docker load 失败' }
    if ($PullBase) {
        & ssh $RemoteHost 'docker pull pgvector/pgvector:pg16'
        if ($LASTEXITCODE -ne 0) { throw '服务器拉取 pgvector 失败' }
    }
    Write-Host "  服务器镜像清单：" -ForegroundColor Green
    & ssh $RemoteHost 'docker images --format "{{.Repository}}:{{.Tag}}  {{.Size}}" | sort'
}
else {
    Step "5/5 已跳过 load（部署时执行：ssh root@IP docker load -i /opt/agent-stack/images/agent-stack-images.tar.gz）"
}

# ---- 6. web 前端静态资源（可选，nginx 托管 /opt/agent-stack/web）----
if ($IncludeWeb) {
    Step "6/6 发布 web 前端（nginx root=/opt/agent-stack/web）"
    if (-not $SkipWebBuild) {
        Write-Host "  本地构建 web（npm run build -> web\dist，含 web\.env 的 API 地址）"
        & npm run build --prefix (Join-Path $repoRoot 'web')
        if ($LASTEXITCODE -ne 0) { throw 'web 构建失败' }
    }
    else {
        Write-Host "  跳过 web 构建，使用现有 web\dist"
    }
    if (-not (Test-Path -LiteralPath $webDir)) { throw "未找到 web 构建产物: $webDir" }

    # tar + gzip（复用之前的流式 gzip 逻辑）
    & tar -cf $webTar -C $webDir .
    if ($LASTEXITCODE -ne 0) { throw 'web tar 失败' }
    $py = @'
import gzip, sys
fin = open(sys.argv[1], 'rb')
fout = gzip.open(sys.argv[2], 'wb')
while True:
    chunk = fin.read(1 << 20)
    if not chunk:
        break
    fout.write(chunk)
fout.close(); fin.close()
'@
    $pyWebFile = Join-Path $outDir '_gz_web_tmp.py'
    [System.IO.File]::WriteAllText($pyWebFile, $py, (New-Object System.Text.UTF8Encoding($false)))
    & python $pyWebFile $webTar $webGz
    if ($LASTEXITCODE -ne 0) { throw 'web gzip 失败' }
    Remove-Item -LiteralPath $pyWebFile -Force
    Remove-Item -LiteralPath $webTar -Force

    # 服务器：备份旧 web -> 解压替换（nginx 静态文件即时生效，无需 reload）
    $webStmp = Get-Date -Format 'yyyyMMdd-HHmmss'
    & scp $webGz "${RemoteHost}:/opt/agent-stack/web-$webStmp.tar.gz"
    if ($LASTEXITCODE -ne 0) { throw 'web scp 上传失败' }
    & ssh $RemoteHost "cd /opt/agent-stack && cp -r web web.prev && rm -rf web.new && mkdir web.new && tar -xzf web-$webStmp.tar.gz -C web.new && rm -rf web && mv web.new web && rm -f web-$webStmp.tar.gz"
    if ($LASTEXITCODE -ne 0) { throw 'web 服务器替换失败' }
    Write-Host "  已发布 web：本地 web\dist -> /opt/agent-stack/web（旧版备份于 web.prev）" -ForegroundColor Green
}

Write-Host ""
Write-Host "[完成] 镜像包: $gzFile" -ForegroundColor Green
if (-not $SkipLoad) {
    Write-Host "[下一步] ssh root@47.108.207.37 -> 部署目录建好 compose + .env.prod -> docker compose up -d（见 P6-D）" -ForegroundColor Cyan
}
