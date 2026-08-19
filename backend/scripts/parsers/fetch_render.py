# fetch_render.py —— 用 Chromium headless 渲染 JS 动态页面并提取正文文本。
#
# 背景：fetch_url 是纯 HTTP 抓取 + HTML 文本提取，对纯 JS 渲染（CSR）页面
# （如 B 站、React/Vue SPA）只能拿到 HTML 骨架，正文在脚本异步加载后才有。
# 本脚本用系统 chromium --headless=new --dump-dom 渲染完成后导出 DOM，再提取
# 正文文本，作为 fetch_url 的渲染版（工具名 fetch_url_render）。
#
# 技术栈：与 render_pdf.py 完全一致的 Chromium headless 运行配置（系统 chromium、
# Alpine musl 无 playwright wheel，直接调系统二进制）。复用了已验证的坑位解法：
#   1. crashpad 可写 database：HOME/XDG/--user-data-dir 指到可写临时目录；
#   2. 派生 uid 无 passwd 条目 → nss_wrapper 动态条目；
#   3. RLIMIT_AS 与 chromium 不兼容 → 沙盒 executor 对 fetch_render 跳过 --as
#      （见 executor.go，与 render_pdf 同处理）。
#
# 关键差异（联网）：本脚本必须联网才能加载外网页面。沙盒默认禁网（unshare -n），
# agent 侧 fetch_url_render 会按会话沙盒配置 network_enabled=true 放行，否则
# chromium 无法解析/加载远程资源（会超时失败）。这是本脚本唯一开放网络的场景。
#
# usage: python3 fetch_render.py <url_abs> <out_txt_abs>
# 成功：写出正文文本文件，stdout 打印 {"ok": true, "bytes": N}
# 失败：emit_error + 退出码 1
import glob
import html
import os
import re
import shutil
import subprocess
import sys

try:
    import pwd  # Unix 专属；Windows 本地跑回归测试时缺失。
except ImportError:  # pragma: no cover - Windows
    pwd = None

import render_common

_CHROMIUM_CANDIDATES = ("/usr/bin/chromium-browser", "/usr/bin/chromium")
_SAFE_VAR = re.compile(r"[a-zA-Z0-9_.]{2,}")  # 内嵌状态变量名（白名单匹配用）

# 正文提取用的噪音标签（与 Go 侧 fetch_url extractPageText 保持一致）。
_SKIP_TAGS = {
    "script", "style", "noscript", "template", "svg", "iframe", "form",
    "nav", "footer", "header",
}


def find_chromium() -> str:
    for path in _CHROMIUM_CANDIDATES:
        if os.path.isfile(path):
            return path
    return _CHROMIUM_CANDIDATES[-1]


def setup_nss_wrapper(tmp_dir: str, env: dict) -> None:
    """与 render_pdf 相同：为派生 uid 提供动态 passwd/group 条目。"""
    try:
        if pwd is None:
            return
        pwd.getpwuid(os.getuid())
        return
    except KeyError:
        pass
    sos = glob.glob("/usr/lib/libnss_wrapper.so*")
    if not sos:
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


def render_dom(chromium: str, url: str, tmp_dir: str, env: dict) -> str:
    """用 chromium --headless=new --dump-dom 渲染页面并返回 DOM 文本。

    返回 (dom_html, stderr)；渲染失败抛 RuntimeError（含 stderr 片段）。
    """
    cmd = [
        chromium,
        "--headless=new",
        "--no-sandbox",
        "--disable-gpu",
        "--disable-dev-shm-usage",
        "--no-first-run",
        "--no-default-browser-check",
        "--user-data-dir=%s" % tmp_dir,
        "--virtual-time-budget=8000",
        "--dump-dom",
        url,
    ]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=55, env=env)
    except (OSError, subprocess.TimeoutExpired) as e:
        raise RuntimeError("fetch_render: 执行 chromium 失败（%s）" % e) from e
    if proc.returncode != 0:
        raise RuntimeError(
            "fetch_render: chromium 渲染失败（exit %s）: %s"
            % (proc.returncode, (proc.stderr or proc.stdout or "")[:500])
        )
    dom = proc.stdout or ""
    if not dom.strip():
        raise RuntimeError("fetch_render: 渲染结果为空（页面可能加载失败或需登录）")
    return dom


def extract_text(dom_html: str) -> str:
    """从渲染后的 DOM 提取正文文本 + 标题，并顺手捞内嵌 JSON 状态（兜底）。

    与 Go 侧 fetch_url extractPageText 逻辑对齐：跳噪音标签、压缩空白。
    """
    import html.parser

    class _Parser(html.parser.HTMLParser):
        def __init__(self):
            super().__init__(convert_charrefs=True)
            self.parts = []
            self.skip_depth = 0
            self.title = ""
            self.in_title = False

        def handle_starttag(self, tag, attrs):
            if tag.lower() in _SKIP_TAGS:
                self.skip_depth += 1
            if tag.lower() == "title":
                self.in_title = True

        def handle_endtag(self, tag):
            if tag.lower() in _SKIP_TAGS:
                self.skip_depth = max(0, self.skip_depth - 1)
            if tag.lower() == "title":
                self.in_title = False

        def handle_data(self, data):
            if self.skip_depth > 0:
                return
            t = data.strip()
            if not t:
                return
            if self.in_title:
                self.title = (self.title + " " + t).strip()
                return
            self.parts.append(t)

    p = _Parser()
    p.feed(dom_html)
    text = " ".join(p.parts)
    embedded = _extract_state(dom_html)
    if embedded and not text:
        text = embedded
    return p.title, text


def _extract_state(dom_html: str) -> str:
    """从 <script> 内嵌 window.XXX= 状态捞可读文本（与 Go 侧同策略，兜底）。"""
    out = []
    for m in re.finditer(
        r"window\.(%s)\s*=\s*(\{[^;]{0,200000})" % _SAFE_VAR.pattern,
        dom_html,
        flags=re.S,
    ):
        var, body = m.group(1), m.group(2)
        if var not in ("__INITIAL_STATE__", "__NEXT_DATA__", "__PRELOADED_STATE__"):
            continue
        import json as _json

        try:
            obj = _json.loads(body)
        except Exception:
            continue
        out.append(_flatten("", obj))
    return "\n".join(x for x in out if x)


def _flatten(prefix: str, v) -> str:
    lines = []
    if isinstance(v, dict):
        for k, val in v.items():
            if isinstance(val, (dict, list)):
                child = ("%s.%s" % (prefix, k)) if prefix else k
                lines.append(_flatten(child, val))
            elif isinstance(val, str) and val.strip():
                lines.append("%s.%s: %s" % (prefix, k, val.strip()) if prefix else "%s: %s" % (k, val.strip()))
    elif isinstance(v, list):
        for i, val in enumerate(v):
            if isinstance(val, (dict, list)):
                lines.append(_flatten("%s[%d]" % (prefix, i), val))
            elif isinstance(val, str) and val.strip():
                lines.append("%s[%d]: %s" % (prefix, i, val.strip()))
    return "\n".join(lines)


def main() -> int:
    if len(sys.argv) != 2:
        sys.stderr.write("usage: python3 fetch_render.py <url>\n")
        sys.exit(2)
    url = sys.argv[1]
    if not url.startswith(("http://", "https://")):
        render_common.emit_error("fetch_render: URL 必须以 http:// 或 https:// 开头")

    chromium = find_chromium()
    if not os.path.isfile(chromium):
        render_common.emit_error(
            "fetch_render: 沙盒缺少 chromium（镜像需 apk add chromium）"
        )

    tmp_dir = os.path.join(os.getcwd(), ".fetch_render_%d" % os.getpid())
    try:
        os.makedirs(tmp_dir, exist_ok=True)
    except OSError as e:
        render_common.emit_error("fetch_render: 创建临时目录失败（%s）" % e)

    env = dict(os.environ)
    env.update(
        {
            "HOME": tmp_dir,
            "XDG_CONFIG_HOME": tmp_dir,
            "XDG_CACHE_HOME": tmp_dir,
        }
    )
    setup_nss_wrapper(tmp_dir, env)

    try:
        dom = render_dom(chromium, url, tmp_dir, env)
    except RuntimeError as e:
        render_common.emit_error(str(e))
    finally:
        shutil.rmtree(tmp_dir, ignore_errors=True)

    title, text = extract_text(dom)
    if not text.strip():
        render_common.emit_error("fetch_render: 渲染完成但未能提取到正文")
    body = ""
    if title:
        body = "## %s\n\n" % title
    body += text
    # 正文直接写到 stdout，供 agent 侧读取（沙盒 ExecResult.Stdout）。
    # 超长截断保护上下文。
    body = body[:20000]
    sys.stdout.write(body + "\n")
    sys.stdout.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())