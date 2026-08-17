# render_common.py —— 文档渲染脚本公共库（render_docx / render_pptx）。
#
# 背景：P4-D 文档生成能力。编排/单智能体的 render_document 工具产出统一的
# DocumentSpec（JSON 中间层），委托沙盒 profile 执行预置渲染脚本，把结构化
# 内容渲染为 Word（.docx）或 PPT（.pptx）文件，产物落用户工作区供 /files 下载。
#
# 统一产物契约（spec.json）：
#   {
#     "format":   "docx" | "pptx",
#     "title":    "文档标题",
#     "subtitle": "副标题（可选）",
#     "sections": [
#       {"heading": "章节标题", "body": "正文（\\n 分段，支持轻量标记）"}
#     ],
#     "footer":   "页脚文本（可选）"
#   }
#
# body 轻量标记约定（两个渲染脚本一致）：
#   - 以 `- ` / `* ` 开头的行 → 列表项
#   - 以 `# ` 开头的行        → 二级标题（docx Heading 2 / ppt 加粗缩进段）
#   - 以 `## ` 开头的行       → 三级标题（docx Heading 3 / ppt 次级缩进段）
#   - 其余行（非空）          → 普通段落
#   - 空行                   → 段落间隔（忽略）
#   - 行内 `**加粗**` 在 docx 里转真实加粗 run（pptx 保持文本原样）
#
# 输出约定：脚本写出目标文件后向 stdout 打印一行 JSON
#   {"ok": true, "bytes": <文件字节数>}     成功
#   {"ok": false, "error": "中文错误说明"}   失败（同时退出码 1）
# 文件显式 chmod 0644：沙盒进程降权到派生 uid（可能非 app 组属主），agent
# （app 用户）经 /files 读取依赖 other 读权限（同 parse 脚本 emit 的做法）。
import json
import os
import re
import sys

# 控制字符过滤：排除 C0/C1 控制符（保留 \t\n\r 供排版）。
_CTRL = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]")


def sanitize_text(text: str) -> str:
    """清理文本：去控制字符、strip 首尾空白。"""
    if text is None:
        return ""
    return _CTRL.sub("", str(text)).strip()


def parse_body(body: str):
    """把 body 拆成渲染块序列：[(kind, text), ...]。
    kind ∈ {p, bullet, h2, h3}。空行跳过；列表项按原文保留缩进层级不做嵌套。
    """
    blocks = []
    for raw in (body or "").splitlines():
        line = sanitize_text(raw)
        if not line:
            continue
        if line.startswith(("## ", "### ")):
            blocks.append(("h3", line.lstrip("# ").strip()))
        elif line.startswith("# "):
            blocks.append(("h2", line.lstrip("# ").strip()))
        elif line.startswith(("- ", "* ")):
            blocks.append(("bullet", line[2:].strip()))
        else:
            blocks.append(("p", line))
    return blocks


def load_spec(spec_path: str) -> dict:
    """读取并校验 spec.json，非法时抛 ValueError（含中文错误说明）。

    P4-J 阶段2起支持富文本块：sections[i].blocks 非空时优先用 blocks，
    blocks 为空时回退用 body（旧契约，向后兼容）。
    """
    try:
        with open(spec_path, encoding="utf-8") as f:
            spec = json.load(f)
    except OSError as e:
        raise ValueError("无法读取 spec.json: %s" % e) from e
    except json.JSONDecodeError as e:
        raise ValueError("spec.json 不是合法 JSON: %s" % e) from e
    if not isinstance(spec, dict):
        raise ValueError("spec.json 顶层必须是 JSON 对象")
    title = sanitize_text(spec.get("title", ""))
    if not title:
        raise ValueError("spec.title 缺失或为空")
    fmt = sanitize_text(spec.get("format", ""))
    if fmt not in ("docx", "pptx"):
        raise ValueError("spec.format 必须为 docx 或 pptx（实际: %r）" % fmt)
    sections = spec.get("sections")
    if not isinstance(sections, list) or not sections:
        raise ValueError("spec.sections 必须是非空数组")
    if len(sections) > 50:
        raise ValueError("spec.sections 超过上限（≤50 节）")
    cleaned = []
    for i, sec in enumerate(sections):
        if not isinstance(sec, dict):
            raise ValueError("spec.sections[%d] 必须是对象" % i)
        heading = sanitize_text(sec.get("heading", ""))
        if not heading:
            raise ValueError("spec.sections[%d].heading 缺失或为空" % i)
        body = sanitize_text(sec.get("body", ""))
        raw_blocks = sec.get("blocks")
        blocks = None
        if isinstance(raw_blocks, list):
            blocks = _validate_blocks(raw_blocks, i)
        if blocks is None and not body:
            raise ValueError("spec.sections[%d] 需要 body 或 blocks 至少一项" % i)
        cleaned.append({"heading": heading, "body": body, "blocks": blocks})
    return {
        "format": fmt,
        "title": title,
        "subtitle": sanitize_text(spec.get("subtitle", "")),
        "sections": cleaned,
        "footer": sanitize_text(spec.get("footer", "")),
    }


_BLOCK_TYPES = {"paragraph", "list", "table", "image", "formula", "code"}


def _validate_blocks(blocks: list, sec_idx: int):
    """校验并规范化 blocks；非法时抛 ValueError。返回 None 表示 blocks 缺失/空。"""
    if not blocks:
        return None
    if len(blocks) > 100:
        raise ValueError("spec.sections[%d].blocks 超过上限（≤100 块）" % sec_idx)
    out = []
    for j, raw in enumerate(blocks):
        if not isinstance(raw, dict):
            raise ValueError("spec.sections[%d].blocks[%d] 必须是对象" % (sec_idx, j))
        btype = sanitize_text(raw.get("type", ""))
        if btype not in _BLOCK_TYPES:
            raise ValueError(
                "spec.sections[%d].blocks[%d].type 非法（支持 %s，实际 %r）"
                % (sec_idx, j, "|".join(sorted(_BLOCK_TYPES)), btype)
            )
        b = {"type": btype}
        if btype == "paragraph":
            b["text"] = sanitize_text(raw.get("text", ""))
            if not b["text"]:
                raise ValueError("spec.sections[%d].blocks[%d].text 缺失" % (sec_idx, j))
        elif btype == "list":
            items = raw.get("items")
            if not isinstance(items, list) or not items or len(items) > 50:
                raise ValueError("spec.sections[%d].blocks[%d].items 需 1~50 项" % (sec_idx, j))
            b["items"] = [sanitize_text(it) for it in items]
            if not any(b["items"]):
                raise ValueError("spec.sections[%d].blocks[%d].items 不能全为空" % (sec_idx, j))
        elif btype == "table":
            headers = raw.get("headers")
            rows = raw.get("rows")
            if not isinstance(headers, list) or not headers or len(headers) > 12:
                raise ValueError("spec.sections[%d].blocks[%d].headers 需 1~12 列" % (sec_idx, j))
            if not isinstance(rows, list) or len(rows) > 100:
                raise ValueError("spec.sections[%d].blocks[%d].rows 超过上限（≤100 行）" % (sec_idx, j))
            ncols = len(headers)
            for r, row in enumerate(rows):
                if not isinstance(row, list) or len(row) != ncols:
                    raise ValueError(
                        "spec.sections[%d].blocks[%d].rows[%d] 列数与表头不一致" % (sec_idx, j, r)
                    )
            b["headers"] = [sanitize_text(h) for h in headers]
            b["rows"] = [[sanitize_text(c) for c in row] for row in rows]
        elif btype == "image":
            src = sanitize_text(raw.get("src", ""))
            if not src:
                raise ValueError("spec.sections[%d].blocks[%d].src 缺失" % (sec_idx, j))
            # 来源白名单：rag-media/ 相对路径（Go 侧解析为绝对路径）或其绝对形态
            # /work/rag-media/…（容器共享卷根），或 svg:// 内联 SVG。
            if not (
                src.startswith("rag-media/")
                or src.startswith("/work/rag-media/")
                or src.startswith("svg://")
            ):
                raise ValueError(
                    "spec.sections[%d].blocks[%d].src 仅支持 rag-media/ 路径或 svg:// 内联 SVG" % (sec_idx, j)
                )
            b["src"] = src
            b["caption"] = sanitize_text(raw.get("caption", ""))
            try:
                w = int(raw.get("width", 0) or 0)
            except (TypeError, ValueError):
                w = 0
            b["width"] = w if 0 <= w <= 2000 else 0
        elif btype == "formula":
            b["text"] = sanitize_text(raw.get("text", ""))
            if not b["text"]:
                raise ValueError("spec.sections[%d].blocks[%d].text（公式）缺失" % (sec_idx, j))
            # 渲染方式（P4-L）：image = matplotlib 图片（默认，稳健）；native = OMML
            # 原生公式（docx 可编辑/可复制，仅支持常见 LaTeX 子集，失败自动回退图片）。
            mode = sanitize_text(raw.get("render", "")).lower()
            if mode not in ("", "image", "native"):
                raise ValueError(
                    "spec.sections[%d].blocks[%d].render 仅支持 image|native（实际 %r）" % (sec_idx, j, mode)
                )
            b["render"] = mode or "image"
        elif btype == "code":
            b["text"] = sanitize_text(raw.get("text", ""))
            b["language"] = sanitize_text(raw.get("language", ""))
            if not b["text"]:
                raise ValueError("spec.sections[%d].blocks[%d].text（代码）缺失" % (sec_idx, j))
        out.append(b)
    return out


def blocks_of(sec: dict):
    """返回章节要渲染的内容块序列；blocks 非空用 blocks，否则把 body 解析为块。"""
    if sec.get("blocks"):
        return sec["blocks"]
    # 旧契约回退：把 body 轻量标记解析为块。
    return [{"type": t, "text": txt} for t, txt in parse_body(sec.get("body", ""))]


def asset_png(src: str, work_dir: str):
    """把图片块 src 解析为可插入的 PNG 绝对路径。

    src 形态（Go 侧 renderDocumentSpec 已把 rag-media 相对路径解析为容器内
    绝对路径并校验存在性，此处仍防御性复查）：
      - /work/rag-media/<docID>/<file>：绝对路径，校验存在后原样返回；
      - rag-media/<docID>/<file>：相对共享卷根（/work），拼成绝对路径；
      - svg://<内联SVG>：写入临时 svg 文件，用 PyMuPDF 转 PNG（沙盒预装）。
    返回 (abs_png, caption, width_px)；失败抛 ValueError（含中文说明）。
    """
    import subprocess
    import tempfile

    if src.startswith("svg://"):
        svg_body = src[len("svg://"):]
        # 内联 SVG 渲染为 PNG：优先 PyMuPDF（已预装，无系统依赖）；
        # 兜底 cairosvg（部分镜像预装），再兜底 ImageMagick convert。
        png_path = None
        try:
            import fitz  # PyMuPDF

            svg_bytes = svg_body.encode("utf-8")
            doc = fitz.open(stream=svg_bytes, filetype="svg")
            page = doc.load_page(0)
            pix = page.get_pixmap(matrix=fitz.Matrix(2, 2), alpha=True)
            png_path = os.path.join(work_dir, "asset_%d.png" % abs(hash(svg_body)))
            pix.save(png_path)
            doc.close()
        except Exception:
            png_path = None
        if not png_path or not os.path.isfile(png_path):
            try:
                import cairosvg

                png_path = os.path.join(work_dir, "asset_%d.png" % abs(hash(svg_body)))
                cairosvg.svg2png(bytestring=svg_body.encode("utf-8"), write_to=png_path, scale=2.0)
            except Exception:
                png_path = None
        if not png_path or not os.path.isfile(png_path) or os.path.getsize(png_path) <= 0:
            raise ValueError("内联 SVG 渲染为图片失败（请检查 SVG 语法）")
        return png_path, None, 0
    if src.startswith("/work/rag-media/"):
        # Go 侧已解析为容器内绝对路径，复查存在性即可。
        if not os.path.isfile(src):
            raise ValueError("知识库图片不存在: %s" % src[:120])
        return src, None, 0
    if src.startswith("rag-media/"):
        # 相对共享卷根（脚本 cwd 是 users/<uid>，须拼 /work 才能命中）。
        abs_path = os.path.join("/work", src)
        if not os.path.isfile(abs_path):
            raise ValueError("知识库图片不存在: %s" % src[:120])
        return abs_path, None, 0
    raise ValueError("不支持的图片来源: %s" % src[:60])


def formula_png(latex: str, work_dir: str):
    """把 LaTeX 公式渲染为透明 PNG（matplotlib mathtext，沙盒需预装 matplotlib）。

    返回 PNG 绝对路径；渲染失败时抛 ValueError。mathtext 支持常用 LaTeX 子集
    （分数/根号/上下标/求和/希腊字母等），教育场景足够。
    进入渲染前先 clean_latex 剥离中文（LLM 常在公式文本里加"LaTeX 公式："等
    中文前缀，mathtext 无法渲染中文 → 乱码方块，P4-L 修复）。
    """
    import tempfile

    latex = clean_latex(latex)
    if not latex:
        raise ValueError("公式为空（剥离中文/空白后无可渲染内容）")

    try:
        import matplotlib

        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError as e:
        raise ValueError("公式渲染引擎未安装（沙盒缺少 matplotlib: %s）" % e) from e

    png_path = os.path.join(work_dir, "formula_%d.png" % abs(hash(latex)))
    # 公式较长时放大字号，保证清晰。
    n = len(latex)
    fontsize = 28 if n <= 40 else 22
    try:
        fig = plt.figure(figsize=(0.01, 0.01))
        fig.text(0, 0, r"${}$".format(latex), fontsize=fontsize)
        fig.savefig(png_path, dpi=200, transparent=True, bbox_inches="tight", pad_inches=0.05)
        plt.close(fig)
    except Exception as e:
        raise ValueError("公式渲染失败（LaTeX 语法错误: %s）" % e) from e
    if not os.path.isfile(png_path) or os.path.getsize(png_path) <= 0:
        raise ValueError("公式渲染产物为空")
    return png_path


def emit_ok(out_path: str, size: int) -> None:
    """成功退出：确保文件可读（0644），打印结果 JSON，退出码 0。"""
    try:
        os.chmod(out_path, 0o644)
    except OSError:
        pass  # best-effort：chown 语义不全的文件系统（如 Docker Desktop bind mount）忽略
    sys.stdout.write(json.dumps({"ok": True, "bytes": size}, ensure_ascii=False) + "\n")
    sys.stdout.flush()
    sys.exit(0)


def emit_error(message: str) -> None:
    """失败退出：打印错误 JSON，退出码 1（调用方读取 stdout 或按退出码判定）。"""
    sys.stderr.write(message + "\n")
    sys.stderr.flush()
    sys.stdout.write(json.dumps({"ok": False, "error": message}, ensure_ascii=False) + "\n")
    sys.stdout.flush()
    sys.exit(1)


def usage(script: str) -> None:
    sys.stderr.write("usage: python3 %s <spec_json_abs> <out_file_abs>\n" % script)
    sys.exit(2)


def file_size(path: str) -> int:
    try:
        return os.path.getsize(path)
    except OSError:
        return -1


# ---------------------------------------------------------------------------
# P4-L 公式质量：中文清理 + 原生公式（LaTeX → OMML，docx/pptx 可编辑公式）。
#
# 背景：LLM 生成公式文本时经常带中文解释前缀（如"LaTeX 公式：x=\\frac{a}{b}"），
# matplotlib mathtext 不认中文 → 图片公式乱码；原生 OMML 公式则可编辑/可复制、
# 放大不失真。clean_latex 统一剥离中文（图片/原生共用），latex_to_omml 把
# LaTeX 常见子集转 OMML（Office Math），docx 直接插 <m:oMath>，pptx 用
# <a14:m> 包装（PPT 2010+）。不支持的 LaTeX 结构自动抛错，调用方回退图片。
# ---------------------------------------------------------------------------

# 公式中的中文字符（CJK 统一表意文字 + CJK 符号 + 全角标点）。mathtext/OMML
# 均无法渲染中文，一律剥离（LLM 的"公式："等解释性前缀也在内）。
_CJK_RE = re.compile(r"[\u2e80-\u9fff\u3000-\u303f\uff00-\uffef]+")


def clean_latex(text: str) -> str:
    """剥离公式文本中的中文解释文字，保留纯 LaTeX 主体。

    策略（按优先级）：
      1. 若含 $...$ / $$...$$ 包裹，取最后一个完整 $...$ 的内容（LLM 常输出
         "$$x=\\frac{a}{b}$$" 或 "LaTeX： $x=1$"）；
      2. 否则剥离全部 CJK 字符与中文标点（含"公式："等前缀）。
    """
    s = sanitize_text(text)
    if not s:
        return ""
    m = re.findall(r"\$\$([^$]+)\$\$|\$([^$]+)\$", s)
    if m:
        s = (m[-1][0] or m[-1][1]).strip()
    s = _CJK_RE.sub("", s)
    # 剥掉剥离后残留的冒号/空格前缀，以及 LaTeX 命令被切开时的孤立 \。
    s = s.strip(" :：，。")
    s = re.sub(r"^\\\s+", "", s)
    return s


# ---- OMML 转换（LaTeX 常见子集 → Office Math Markup Language）----
_MATH_NS = "http://schemas.openxmlformats.org/officeDocument/2006/math"
_A14_NS = "http://schemas.microsoft.com/office/drawing/2010/main"

# LaTeX 命令 → 单个字符（希腊字母/运算符）。
_CMD_CHARS = {
    "alpha": "\u03b1", "beta": "\u03b2", "gamma": "\u03b3", "delta": "\u03b4",
    "epsilon": "\u03b5", "zeta": "\u03b6", "eta": "\u03b7", "theta": "\u03b8",
    "iota": "\u03b9", "kappa": "\u03ba", "lambda": "\u03bb", "mu": "\u03bc",
    "nu": "\u03bd", "xi": "\u03be", "pi": "\u03c0", "rho": "\u03c1",
    "sigma": "\u03c3", "tau": "\u03c4", "upsilon": "\u03c5", "phi": "\u03c6",
    "chi": "\u03c7", "psi": "\u03c8", "omega": "\u03c9",
    "Gamma": "\u0393", "Delta": "\u0394", "Theta": "\u0398", "Lambda": "\u039b",
    "Xi": "\u039e", "Pi": "\u03a0", "Sigma": "\u03a3", "Phi": "\u03a6",
    "Psi": "\u03a8", "Omega": "\u03a9",
    "pm": "\u00b1", "times": "\u00d7", "div": "\u00f7", "cdot": "\u00b7",
    "le": "\u2264", "ge": "\u2265", "neq": "\u2260", "approx": "\u2248",
    "infty": "\u221e", "rightarrow": "\u2192", "to": "\u2192",
    "ldots": "\u2026", "cdots": "\u22ef", "partial": "\u2202", "nabla": "\u2207",
    "in": "\u2208", "notin": "\u2209", "subset": "\u2282", "subseteq": "\u2286",
    "supset": "\u2283", "cup": "\u222a", "cap": "\u2229",
    "forall": "\u2200", "exists": "\u2203", "neg": "\u00ac",
    "vert": "\u2223", "mid": "\u2223", "langle": "\u27e8", "rangle": "\u27e9",
    "bullet": "\u2022",
}
# 求和/积分等大运算符（渲染为 OMML nary）。
_CMD_NARY = {"sum": "\u2211", "prod": "\u220f", "int": "\u222b", "iint": "\u222c"}
# 常见函数名（OMML 以普通文本 run 输出，Office 默认字体即斜体函数样式）。
_KNOWN_FUNCS = {"sin", "cos", "tan", "cot", "sec", "csc", "log", "ln", "exp",
                "lim", "max", "min", "gcd", "arg", "deg", "mod"}


def _tokenize(latex: str):
    """LaTeX → token 流：('cmd', name) 命令 / ('{','_','^','}') 结构符 / ('char', c)。"""
    toks = []
    i, n = 0, len(latex)
    while i < n:
        c = latex[i]
        if c == "\\":
            if i + 1 >= n:
                i += 1
                continue
            nxt = latex[i + 1]
            if nxt.isalpha():
                j = i + 1
                while j < n and latex[j].isalpha():
                    j += 1
                toks.append(("cmd", latex[i + 1:j]))
                i = j
            else:
                toks.append(("char", nxt))  # \, \; 等转义符作为普通字符
                i += 2
        elif c in "{}^_":
            toks.append((c, None))
            i += 1
        else:
            toks.append(("char", c))
            i += 1
    return toks


def _parse_seq(toks, pos):
    """解析到 '}' 或末尾，返回 (片段列表, 下一位置)。"""
    frags = []
    while pos < len(toks):
        kind, _ = toks[pos]
        if kind == "}":
            return frags, pos + 1
        next_pos = pos
        atom, next_pos = _parse_atom(toks, pos)
        # 防御：原子解析必须推进位置，否则强制跳过一个 token，保证终止
        #（未知命令/孤立结构符等非原子 token 被安全跳过）。
        if next_pos <= pos:
            next_pos = pos + 1
        pos = next_pos
        if atom is not None:
            frags.append(atom)
    return frags, pos


def _parse_script_arg(toks, pos):
    """解析一个脚本参数（{...} 分组或单原子）。返回 (片段, 下一位置)。"""
    if pos < len(toks) and toks[pos][0] == "{":
        inner, pos = _parse_seq(toks, pos + 1)
        return ("grp", inner), pos
    return _parse_atom(toks, pos)


def _attach_scripts(toks, pos, frag):
    """原子解析后检查 ^ / _ 后缀，附着上标/下标。返回 (最终片段, 下一位置)。"""
    scripts = []
    while pos < len(toks) and toks[pos][0] in ("^", "_"):
        is_sup = toks[pos][0] == "^"
        arg, pos = _parse_script_arg(toks, pos + 1)
        scripts.append((is_sup, arg))
    if not scripts:
        return frag, pos
    if frag[0] == "nary":
        # 大运算符（∑/∫）：sub/sup 作为其上下限（OMML nary 子元素）。
        sub = sup = None
        for is_sup, arg in scripts:
            if is_sup:
                sup = arg
            else:
                sub = arg
        return ("nary", frag[1], sub, sup), pos
    if len(scripts) == 1:
        is_sup, arg = scripts[0]
        return ("sup" if is_sup else "sub", frag, arg), pos
    # 两个脚本（x_i^2 / x^2_i）：合成 sSubSup。
    (s1, a1), (s2, a2) = scripts[:2]
    sub = a1 if not s1 else (a2 if not s2 else None)
    sup = a1 if s1 else (a2 if s2 else None)
    return ("ssubsup", frag, sub, sup), pos


def _parse_delim(toks, pos):
    r"""\left<delim> ... \right<delim> → ("d", beg, inner, end)，OMML 自动缩放括号。"""
    pos2 = pos + 1
    beg = "("
    if pos2 < len(toks) and toks[pos2][0] == "char":
        beg = toks[pos2][1]
        pos2 += 1
    inner = []
    end = ")"
    while pos2 < len(toks):
        k, v = toks[pos2]
        if k == "cmd" and v == "right":
            pos2 += 1
            if pos2 < len(toks) and toks[pos2][0] == "char":
                end = toks[pos2][1]
                pos2 += 1
            break
        atom, pos2 = _parse_atom(toks, pos2)
        if atom is not None:
            inner.append(atom)
        else:
            pos2 += 1  # 防御：非原子 token 强制跳过，保证终止
    return ("d", beg, inner, end), pos2


def _parse_atom(toks, pos):
    """解析一个原子（命令/分组/单字符），返回 (片段, 下一位置)。"""
    if pos >= len(toks):
        return None, pos
    kind, val = toks[pos]
    if kind == "}":
        return None, pos
    if kind == "{":
        inner, pos = _parse_seq(toks, pos + 1)
        frag = ("grp", inner)
    elif kind == "cmd":
        if val == "frac":
            num, pos = _parse_script_arg(toks, pos + 1)
            den, pos = _parse_script_arg(toks, pos)
            frag = ("f", num, den)
        elif val == "sqrt":
            pos2 = pos + 1
            deg = None
            if pos2 < len(toks) and toks[pos2][0] == "[":
                # \sqrt[n]{x}：n 是 deg（普通分组）。
                inner, pos2 = _parse_seq(toks, pos2 + 1)
                deg = ("grp", inner)
            rad, pos = _parse_script_arg(toks, pos2)
            frag = ("rad", deg, rad)
        elif val in _CMD_NARY:
            frag = ("nary", _CMD_NARY[val], None, None)
            pos += 1  # 大运算符本身是单个 token：跳过后再检查 ^/_ 脚本
        elif val == "left":
            return _parse_delim(toks, pos)
        elif val in _CMD_CHARS:
            frag = ("r", _CMD_CHARS[val])
            pos += 1
        elif val in _KNOWN_FUNCS:
            frag = ("fn", val)
            pos += 1
        elif val in ("text", "operatorname", "mathrm"):
            # \text{中文/说明}：内部按普通字符序列输出（OMML run）。
            pos2 = pos + 1
            if pos2 < len(toks) and toks[pos2][0] == "{":
                inner, pos2 = _parse_seq(toks, pos2 + 1)
                frag = ("grp", inner)
                pos = pos2
                return _attach_scripts(toks, pos, frag)
            pos += 1  # 无参 \text：跳过命令 token（保证推进）
            frag = None
        else:
            pos += 1  # 未知命令：跳过该 token（保证推进，防死循环）
            frag = None  # 忽略（如 \, \quad 已被 tokenize 处理）
    else:
        frag = ("r", val)
        pos += 1  # 单字符原子：跳过后再检查 ^/_ 脚本
    return _attach_scripts(toks, pos, frag)


def _esc(s: str) -> str:
    import xml.sax.saxutils as su
    return su.escape(s).replace('"', "&quot;")


def _omml_frag(frag) -> str:
    t = frag[0]
    if t == "r":
        return "<m:r><m:t>%s</m:t></m:r>" % _esc(frag[1])
    if t == "fn":
        return "<m:r><m:t>%s</m:t></m:r>" % _esc(frag[1])
    if t == "grp":
        return "".join(_omml_frag(x) for x in frag[1])
    if t == "f":
        return ("<m:f><m:num>%s</m:num><m:den>%s</m:den></m:f>"
                % (_omml_frag(frag[1]), _omml_frag(frag[2])))
    if t == "sup":
        return ("<m:sSup><m:e>%s</m:e><m:sup>%s</m:sup></m:sSup>"
                % (_omml_frag(frag[1]), _omml_frag(frag[2])))
    if t == "sub":
        return ("<m:sSub><m:e>%s</m:e><m:sub>%s</m:sub></m:sSub>"
                % (_omml_frag(frag[1]), _omml_frag(frag[2])))
    if t == "ssubsup":
        return ("<m:sSubSup><m:e>%s</m:e><m:sub>%s</m:sub><m:sup>%s</m:sup></m:sSubSup>"
                % (_omml_frag(frag[1]), _omml_frag(frag[2]), _omml_frag(frag[3])))
    if t == "rad":
        deg, base = frag[1], frag[2]
        if deg is None:
            return ('<m:rad><m:radPr><m:degHide m:val="1"/></m:radPr>'
                    "<m:deg/><m:e>%s</m:e></m:rad>" % _omml_frag(base))
        return "<m:rad><m:deg>%s</m:deg><m:e>%s</m:e></m:rad>" % (_omml_frag(deg), _omml_frag(base))
    if t == "nary":
        _, ch, sub, sup = frag
        sub_xml = _omml_frag(sub) if sub is not None else "<m:sub/>"
        sup_xml = _omml_frag(sup) if sup is not None else "<m:sup/>"
        return ('<m:nary><m:naryPr><m:chr m:val="%s"/></m:naryPr>'
                "<m:sub>%s</m:sub><m:sup>%s</m:sup><m:e>%s</m:e></m:nary>"
                % (_esc(ch), sub_xml, sup_xml, _omml_frag(frag[4]) if len(frag) > 4 else ""))
    if t == "d":
        _, beg, inner, end = frag
        return ('<m:d><m:dPr><m:begChr m:val="%s"/><m:endChr m:val="%s"/></m:dPr>'
                "<m:e>%s</m:e></m:d>"
                % (_esc(beg), _esc(end), "".join(_omml_frag(x) for x in inner)))
    return ""


def latex_to_omml(latex: str) -> str:
    r"""LaTeX（常见子集）→ OMML <m:oMath> XML 字符串。失败抛 ValueError。

    支持：\frac{}{}、^/_ 上下标、\sqrt{}/\sqrt[n]{}、\sum/\prod/\int 及上下限、
    \left(...\right) 自动缩放括号、希腊字母、常用运算符（±×÷≤≥≠≈∞→）、
    常见函数名（sin/log/lim…）。其余结构忽略或抛错，由调用方回退图片渲染。
    """
    latex = clean_latex(latex)
    if not latex:
        raise ValueError("公式为空")
    toks = _tokenize(latex)
    frags, _ = _parse_seq(toks, 0)
    body = "".join(_omml_frag(x) for x in frags)
    if not body:
        raise ValueError("公式转换结果为空（LaTeX 结构不支持）")
    return '<m:oMath xmlns:m="%s">%s</m:oMath>' % (_MATH_NS, body)


def omml_into_docx_paragraph(paragraph, omml_xml: str) -> None:
    """把 OMML <m:oMath> 追加到 python-docx 段落末尾（公式与文字同行）。"""
    from docx.oxml import parse_xml

    paragraph._p.append(parse_xml(omml_xml))


def omml_into_pptx_paragraph(tf_paragraph, omml_xml: str) -> None:
    """把 OMML 公式以 <a14:m> 包装追加到 python-pptx 文本框段落（PPT 2010+）。"""
    from lxml import etree

    xml = '<a14:m xmlns:a14="%s" xmlns:m="%s">%s</a14:m>' % (_A14_NS, _MATH_NS, omml_xml)
    tf_paragraph._p.append(etree.fromstring(xml.encode("utf-8")))
