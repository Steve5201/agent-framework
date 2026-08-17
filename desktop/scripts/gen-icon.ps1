# ---------------------------------------------------------------------------
# 生成应用图标源图（1024x1024 PNG）
# 用法：powershell -File scripts/gen-icon.ps1   → 输出 ./app-icon.png
# 之后执行 `npm run tauri icon app-icon.png` 生成 src-tauri/icons 全套格式。
#
# 说明：这是占位设计（深色底 + 聊天气泡 + 三点），后续换正式 Logo 时
#       直接改本脚本的绘制部分，或替换 app-icon.png 后重新跑 icon 命令。
# ---------------------------------------------------------------------------
Add-Type -AssemblyName System.Drawing

$out = Join-Path $PSScriptRoot '..\app-icon.png'
$size = 1024

$bmp = [System.Drawing.Bitmap]::new($size, $size)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias

# 背景：石板色对角渐变
$rect = [System.Drawing.Rectangle]::new(0, 0, $size, $size)
$c1 = [System.Drawing.Color]::FromArgb(255, 30, 41, 59)   # slate-800
$c2 = [System.Drawing.Color]::FromArgb(255, 2, 6, 23)     # slate-950
$bgBrush = [System.Drawing.Drawing2D.LinearGradientBrush]::new($rect, $c1, $c2, 45.0)
$g.FillRectangle($bgBrush, $rect)

# 圆角矩形路径辅助函数
function New-RoundedRectPath([int]$x, [int]$y, [int]$w, [int]$h, [int]$r) {
    $p = [System.Drawing.Drawing2D.GraphicsPath]::new()
    $d = $r * 2
    $p.AddArc($x, $y, $d, $d, 180, 90)
    $p.AddArc($x + $w - $d, $y, $d, $d, 270, 90)
    $p.AddArc($x + $w - $d, $y + $h - $d, $d, $d, 0, 90)
    $p.AddArc($x, $y + $h - $d, $d, $d, 90, 90)
    $p.CloseFigure()
    return $p
}

# 聊天气泡（白色圆角矩形 + 三角尾巴）
$white = [System.Drawing.SolidBrush]::new([System.Drawing.Color]::White)
$bubble = New-RoundedRectPath 192 224 640 448 120
$g.FillPath($white, $bubble)
$tail = [System.Drawing.Point[]]@(
    [System.Drawing.Point]::new(320, 660),
    [System.Drawing.Point]::new(320, 810),
    [System.Drawing.Point]::new(470, 660)
)
$g.FillPolygon($white, $tail)

# 气泡内三个点（蓝色系，寓意 Agent 对话）
$blue = [System.Drawing.SolidBrush]::new([System.Drawing.Color]::FromArgb(255, 59, 130, 246))
foreach ($cx in @(352, 512, 672)) {
    $g.FillEllipse($blue, $cx - 48, 400, 96, 96)
}

$g.Dispose()
$bmp.Save($out, [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()
Write-Host "icon source generated: $out"
