# render_pdf.py —— 把自包含 HTML 文档渲染为 PDF（HTML 中间层文档生成的 PDF 导出）。
#
# 背景：render_html 工具（format=pdf）落盘 HTML 后委托沙盒本脚本做 HTML→PDF。
# 技术栈：Chromium headless 命令行 `--headless=new --print-to-pdf`（Chromium 官方
# PDF 打印管线，业界标准）。沙盒镜像用 `apk add chromium` 安装系统 chromium：
# Alpine 是 musl 构建，playwright 等 Python 库无 musllinux wheel 无法 pip 安装，
# 故不引入任何 Python 渲染依赖，直接用系统二进制最稳。
#
# 已知坑（业界确认）：
#   1. Chromium 128+ crashpad 强制要求可写 database 目录，受限容器（只读 rootfs）
#      下默认目录不可写会崩溃（--database is required）。解法：把 HOME /
#      XDG_CONFIG_HOME / XDG_CACHE_HOME 与 --user-data-dir 全部指到与输出同目录的
#      可写临时目录（沙盒执行用户是工作区属主，可写）。
#   2. 沙盒执行进程是派生 uid（2000+user_id），/etc/passwd 无对应条目；chromium
#      非 root 且 getpwuid 失败时无法解析 home → crashpad database 路径为空 →
#      handler 报错导致 SIGTRAP。解法：nss_wrapper（沙盒镜像预装）提供动态
#      passwd/group 条目（业界标准解法）。
#   3. RLIMIT_AS 与 chromium 不兼容（Alpine/musl 下设置即崩，与值大小无关）：
#      sandbox executor 对 render_pdf 跳过 --as 限制（见 executor.go）。
#
# 安全（LLM 产出的 HTML 是不可信输入，本脚本是第三道防线）：
#   - 渲染前剥除 <script>/<iframe>（Go 侧 sanitizeHTML 已净化，此处兜底；
#     headless 命令行无法禁用 JS，剥除后 HTML 无脚本来源）；
#   - 沙盒执行进程禁网（unshare -n），外部资源请求直接失败，不产生数据外带；
#   - --virtual-time-budget 限制渲染等待，防外部资源拖慢卡死。
#
# 缺 chromium/渲染失败：一律 emit_error + 退出码 1，由 Go 侧降级为 HTML（主链路
# 不阻断）。
#
# usage: python3 render_pdf.py <input_html_abs> <output_pdf_abs>
import glob
import html
import os
import re
import shutil
import subprocess
import sys

try:
    import pwd  # Unix 专属：仅 setup_nss_wrapper 使用；Windows 本地跑回归测试时缺失。
except ImportError:  # pragma: no cover - Windows
    pwd = None

import render_common

# 系统 chromium 候选路径（Alpine: /usr/bin/chromium；Debian 系: chromium-browser）。
_CHROMIUM_CANDIDATES = ("/usr/bin/chromium-browser", "/usr/bin/chromium")


def find_chromium() -> str:
    for path in _CHROMIUM_CANDIDATES:
        if os.path.isfile(path):
            return path
    return _CHROMIUM_CANDIDATES[-1]


def setup_nss_wrapper(tmp_dir: str, env: dict) -> None:
    """降权派生 uid 无 /etc/passwd 条目时，为 chromium 提供动态 passwd/group。

    沙盒执行进程 uid = 2000 + user_id，容器 /etc/passwd 无对应条目；chromium 非
    root 且 getpwuid 失败时无法解析 home → crashpad database 路径为空 → handler
    报错致 SIGTRAP。nss_wrapper（镜像预装）经 LD_PRELOAD 接管 NSS 查询，业界标准
    解法。当前 uid 已有条目（root/常规用户）时无需处理。
    """
    try:
        if pwd is None:
            return  # 非 Unix 环境（Windows 本地测试）无 nss_wrapper 语义。
        pwd.getpwuid(os.getuid())
        return
    except KeyError:
        pass
    sos = glob.glob("/usr/lib/libnss_wrapper.so*")
    if not sos:
        # 未装 nss_wrapper：保持原样，chromium 后续失败会由 emit_error 上报。
        return
    uid = os.getuid()
    try:
        with open(os.path.join(tmp_dir, "passwd"), "w", encoding="utf-8") as f:
            f.write("sandbox:x:%d:%d:,,,:%s:/sbin/nologin\n" % (uid, uid, tmp_dir))
        with open(os.path.join(tmp_dir, "group"), "w", encoding="utf-8") as f:
            f.write("sandbox:x:%d:\n" % uid)
    except OSError:
        return
    env["NSS_WRAPPER_PASSWD"] = os.path.join(tmp_dir, "passwd")
    env["NSS_WRAPPER_GROUP"] = os.path.join(tmp_dir, "group")
    env["LD_PRELOAD"] = sos[0]


def strip_dangerous(html_text: str) -> str:
    """再次剥除 script/iframe 节点（含自闭合/带属性形态）。防御性兜底。"""
    html_text = re.sub(r"<script\b[^>]*>.*?</script>", "", html_text, flags=re.S | re.I)
    html_text = re.sub(r"<script\b[^>]*/>", "", html_text, flags=re.I)
    html_text = re.sub(r"<iframe\b[^>]*>.*?</iframe>", "", html_text, flags=re.S | re.I)
    html_text = re.sub(r"<iframe\b[^>]*/>", "", html_text, flags=re.I)
    return html_text


# 打印增强（P5-HTML 实测修复）：
#   1. 打印背景：Chromium --print-to-pdf 等价浏览器打印对话框"背景图形"未勾选，
#      默认丢弃背景渲染——HTML 里的 linear-gradient 渐变/背景色/背景图全部丢失。
#      命令行无 --print-background flag，业界标准做法是注入 print-color-adjust:
#      exact 强制打印背景。
#   2. 字体强制覆盖（崩溃根因修复）：中文（CJK）字形在无 CJK 覆盖的字体上做
#      回退时，Chromium 打印管线会崩溃（print_render_frame_helper.cc "Printing
#      failed"，产物为空）。容器仅 font-noto-cjk（Noto CJK 家族）+ chromium 自带
#      Open Sans；"Noto Sans Mono CJK SC" 在任何上下文都会直接触发该崩溃；
#      generic monospace + 中文会走慢速回退（实测 55s 超时）。因此全量强制
#      "Noto Sans CJK SC" 为首选 —— 消除回退，既不崩溃也不超时。
#   3. 页眉页脚：thead/tfoot 在 Chromium 打印时每页自动重复，且位于正常文档流
#      （不与正文重叠、支持背景色与 CJK），是 headless --print-to-pdf 下唯一
#      可靠的每页页眉页脚方案（实测 position:fixed 相对内容区定位会遮挡正文，
#      负偏移会错位，内置 header/footer 遇 CJK 标题会崩溃）。
_PRINT_CSS = """<style>
  * {
    -webkit-print-color-adjust: exact !important;
    print-color-adjust: exact !important;
  }
  * {
    font-family: "Noto Sans CJK SC", "Open Sans", sans-serif !important;
  }
  table.doc-shell { width: 100%; border-collapse: collapse; }
  table.doc-shell > thead > tr > td,
  table.doc-shell > tfoot > tr > td,
  table.doc-shell > tbody > tr > td { border: none; padding: 0; }
  table.doc-shell > thead > tr > td {
    background: #0984e3 !important;
    color: #ffffff !important;
    text-align: center;
    padding: 4mm 6mm !important;
    font-size: 14pt;
  }
  table.doc-shell > tfoot > tr > td {
    background: #eef4fd !important;
    color: #64748b !important;
    text-align: center;
    padding: 3mm 6mm !important;
    font-size: 10pt;
  }
</style>
"""


def inject_print_css(html_text: str) -> str:
    """注入打印增强 CSS（字体覆盖 + 背景 + 页眉页脚表壳样式）到 <head>。"""
    text, n = re.subn(
        r"</head>", _PRINT_CSS + "\n</head>", html_text, count=1, flags=re.I
    )
    if n == 0:
        text, n = re.subn(
            r"</body>", _PRINT_CSS + "\n</body>", html_text, count=1, flags=re.I
        )
    if n == 0:
        text = html_text + "\n" + _PRINT_CSS
    return text


def extract_title(html_text: str) -> str:
    """从 <title> 或首个 <h1> 提取文档标题（页眉文字），兜底"智能文档"。"""
    for pat in (r"<title[^>]*>(.*?)</title>", r"<h1[^>]*>(.*?)</h1>"):
        m = re.search(pat, html_text, flags=re.S | re.I)
        if not m:
            continue
        t = re.sub(r"<[^>]+>", "", m.group(1)).strip()
        if t:
            return t[:60]
    return "智能文档"


def wrap_body_with_header_footer(html_text: str, title: str) -> str:
    """把 <body> 内容包进 table.doc-shell（thead=页眉 / tfoot=页脚）。

    thead/tfoot 在 Chromium 打印时每页自动重复。tbody 单元格内嵌套用户原有
    的 <table> 合法，不受表壳样式影响（样式仅作用于壳的直接子单元格）。
    无 <body> 时保持原样（仍注入字体/背景 CSS）。
    """
    hd = html.escape(title)
    ft = "智能助手生成 · 内部资料"
    shell_open = (
        '<table class="doc-shell">\n'
        "  <thead><tr><td>%s</td></tr></thead>\n"
        "  <tfoot><tr><td>%s</td></tr></tfoot>\n"
        "  <tbody><tr><td>\n" % (hd, ft)
    )
    shell_close = "\n  </td></tr></tbody>\n</table>\n"
    m = re.search(r"<body[^>]*>", html_text, flags=re.I)
    if not m:
        return html_text
    open_pos = m.end()
    cm = re.search(r"</body\s*>", html_text[open_pos:], flags=re.I)
    close_pos = open_pos + cm.start() if cm else len(html_text)
    return (
        html_text[:open_pos]
        + shell_open
        + html_text[open_pos:close_pos]
        + shell_close
        + html_text[close_pos:]
    )


def main() -> int:
    if len(sys.argv) != 3:
        render_common.usage("render_pdf.py")
    html_path, out_path = sys.argv[1], sys.argv[2]

    try:
        with open(html_path, encoding="utf-8") as f:
            html_text = f.read()
    except OSError as e:
        render_common.emit_error("render_pdf: 读取 HTML 失败（%s）" % e)
    if not html_text.strip():
        render_common.emit_error("render_pdf: HTML 内容为空")
    html_text = strip_dangerous(html_text)
    # 打印增强：字体覆盖（CJK 回退崩溃）+ 强制打印背景 + 每页页眉页脚。
    title = extract_title(html_text)
    html_text = inject_print_css(html_text)
    html_text = wrap_body_with_header_footer(html_text, title)

    chromium = find_chromium()
    if not os.path.isfile(chromium):
        render_common.emit_error(
            "render_pdf: 沙盒缺少 chromium（镜像需 apk add chromium + font-noto-cjk）"
        )

    # 净化后的 HTML 写临时文件渲染（原落盘 HTML 属 agent 进程、沙盒只读）。
    tmp_html = out_path + ".sanitized.html"
    try:
        with open(tmp_html, "w", encoding="utf-8") as f:
            f.write(html_text)
    except OSError as e:
        render_common.emit_error("render_pdf: 写入净化 HTML 失败（%s）" % e)

    # 可写临时目录（容器 rootfs 只读；crashpad/XDG/用户数据目录都落这里）。
    tmp_dir = out_path + ".chromium"
    try:
        os.makedirs(tmp_dir, exist_ok=True)
    except OSError as e:
        render_common.emit_error("render_pdf: 创建临时目录失败（%s）" % e)

    env = dict(os.environ)
    env.update(
        {
            "HOME": tmp_dir,
            "XDG_CONFIG_HOME": tmp_dir,
            "XDG_CACHE_HOME": tmp_dir,
        }
    )
    # 派生 uid 无 passwd 条目 → nss_wrapper 提供动态条目（chromium/crashpad 必需）。
    setup_nss_wrapper(tmp_dir, env)

    cmd = [
        chromium,
        "--headless=new",
        "--no-sandbox",
        "--disable-gpu",
        "--disable-dev-shm-usage",
        "--user-data-dir=%s" % tmp_dir,
        "--no-pdf-header-footer",
        "--virtual-time-budget=2000",
        "--print-to-pdf=%s" % out_path,
        "file://" + tmp_html,
    ]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=55, env=env)
    except (OSError, subprocess.TimeoutExpired) as e:
        render_common.emit_error("render_pdf: 执行 chromium 失败（%s）" % e)
    finally:
        # dbus/vulkan 等 stderr 噪音无害，仅失败时取前 500 字符上报。
        shutil.rmtree(tmp_dir, ignore_errors=True)
        try:
            os.remove(tmp_html)
        except OSError:
            pass
    if proc.returncode != 0:
        render_common.emit_error(
            "render_pdf: chromium 渲染失败（exit %s）: %s"
            % (proc.returncode, (proc.stderr or proc.stdout or "")[:500])
        )

    size = render_common.file_size(out_path)
    if size <= 0:
        render_common.emit_error("render_pdf: 产物为空或不可读")
    render_common.emit_ok(out_path, size)
    return 0


if __name__ == "__main__":
    sys.exit(main())
