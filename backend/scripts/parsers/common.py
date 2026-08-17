# common.py —— sandbox 解析脚本公共库（P3-A3b）。
#
# 三个解析脚本（parse_pdf / parse_docx / parse_pptx）统一产出契约：
#   {
#     "title":    string,   # 文档标题（尽力而为）
#     "markdown": string,   # 结构化 markdown：标题(##)分段 + 正文 + 图片占位
#     "media":    [{ "type": "image|video|audio", "path": "rag-media/<doc_id>/<file>", "alt": "..." }],
#     "scan_only": bool,    # PDF 无文本层（扫描版）
#     "warnings":  [string, ...]
#   }
#
# 路径约定（沙盒执行 cwd = /work/users/<user_id>）：
#   - 输入/输出用绝对路径（rag 与 sandbox 共享 /work 卷，路径一致）
#   - markdown 中的媒体引用与 media.path 用「公共引用路径」
#     rag-media/<doc_id>/<file>：位于共享卷根公共只读区（P3-A8，所有用户可经
#     agent /files 读取），引用路径与物理落盘位置解耦（见 media_rel_prefix）。
import json
import os
import re
import sys
import tempfile
from urllib.parse import unquote

# media 公共引用前缀（恒为 rag-media/<doc_id>，POSIX 分隔符）。
def media_rel_prefix(media_dir_abs: str) -> str:
    """把 media 绝对目录转成公共引用前缀。

    取路径末两级目录名（父目录名 + doc_id）而非相对 cwd 的 relpath：早期布局
    users/<uid>/rag-media/<doc_id> 与现在公共只读区 <WorkRoot>/rag-media/<doc_id>
    物理位置不同、相对 cwd 的 relpath 也会不同，但引用前缀必须稳定为
    rag-media/<doc_id>（DB 产物与模型输出 /files URL 都依赖它）。
    """
    norm = os.path.normpath(media_dir_abs)
    leaf = os.path.basename(norm)                       # <doc_id>
    parent = os.path.basename(os.path.dirname(norm))    # rag-media
    return (parent + "/" + leaf).rstrip("/")


# 文件名校验：只允许字母数字 _ - .（防路径穿越/换行注入）。
_SAFE_NAME = re.compile(r"^[A-Za-z0-9._-]+$")


def safe_filename(name: str) -> str:
    """清理文件名（去路径、URL 解码、保留安全字符）。"""
    name = unquote(name)
    name = os.path.basename(name)
    name = re.sub(r"[^A-Za-z0-9._-]", "_", name)
    if not _SAFE_NAME.match(name):
        name = "file.bin"
    return name


# 媒体类型：按扩展名归类（供 media 清单）。
_IMG_EXT = {".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".svg", ".tif", ".tiff"}
_VIDEO_EXT = {".mp4", ".mov", ".avi", ".webm", ".mkv", ".wmv", ".flv"}
_AUDIO_EXT = {".mp3", ".wav", ".aac", ".ogg", ".m4a", ".flac"}


def media_type(name: str) -> str:
    ext = os.path.splitext(name)[1].lower()
    if ext in _IMG_EXT:
        return "image"
    if ext in _VIDEO_EXT:
        return "video"
    if ext in _AUDIO_EXT:
        return "audio"
    return "other"


def ensure_media_dir(media_dir_abs: str) -> None:
    """创建媒体目录并放宽权限（0777），保证 rag（app uid）可递归清理。

    目录属主是降权解析进程（uid=派生 uid），rag 删除文档时需写权限才能
    递归清理（cleanupMedia 按 rag-media/<doc_id> 删除）：
      - 文档媒体目录本身（rag-media/<doc_id>/）：删其中的媒体文件；
      - 父级 rag-media/：摘除 <doc_id> 目录条目。
    两者都 chmod 0777。沙盒内均属可信进程，放宽到 other 可写在单机部署
    下可接受（rag-media 本就按 other 可读设计，供 rag /files 静态服务）。
    """
    os.makedirs(media_dir_abs, exist_ok=True)
    os.chmod(media_dir_abs, 0o777)
    parent = os.path.dirname(media_dir_abs)
    if parent and os.path.isdir(parent):
        try:
            os.chmod(parent, 0o777)
        except OSError:
            # 公共区父目录（<WorkRoot>/rag-media）由 sandbox 主进程（root）预创建为
            # 0777，解析进程非属主无权再 chmod —— best-effort 跳过，不影响写入。
            pass


def save_bytes(data: bytes, media_dir_abs: str, name: str, prefix: str) -> str:
    """把二进制媒体写入 media 目录（防重名），返回公共引用路径。

    文件显式 chmod 0644：解析进程的 umask 可能把新文件落在 0600，其它用户经
    agent /files 读取会 403（对话渲染 KB 媒体失败），故写入后强制放宽可读。
    """
    ensure_media_dir(media_dir_abs)
    name = safe_filename(name)
    base, ext = os.path.splitext(name)
    candidate = os.path.join(media_dir_abs, name)
    i = 1
    while os.path.exists(candidate):
        candidate = os.path.join(media_dir_abs, "%s_%d%s" % (base, i, ext))
        i += 1
    with open(candidate, "wb") as f:
        f.write(data)
    os.chmod(candidate, 0o644)
    return os.path.join(prefix, os.path.basename(candidate)).replace(os.sep, "/")


def emit(out_path: str, title: str, markdown: str, media: list, scan_only: bool, warnings: list) -> None:
    """写出统一 JSON 产物。"""
    payload = {
        "title": title,
        "markdown": markdown,
        "media": media,
        "scan_only": bool(scan_only),
        "warnings": warnings,
    }
    tmp_fd, tmp_path = tempfile.mkstemp(suffix=".json", dir=os.path.dirname(out_path))
    try:
        with os.fdopen(tmp_fd, "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False)
        # mkstemp 默认 0600，rag（app 组/other）需读回产物 → 放宽为 0644。
        os.chmod(tmp_path, 0o644)
        os.replace(tmp_path, out_path)  # 原子写，防 rag 读到半截文件
    finally:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)


def load_input(path: str) -> bytes:
    with open(path, "rb") as f:
        return f.read()


def rewrite_media_refs(markdown: str, media_ref: str) -> str:
    """把 pandoc/python 产出的媒体引用统一改写成相对 cwd 的持久路径。

    支持 ![alt](media/xxx)（pandoc extract-media）与 ![alt](xxx)（PPT 提取）。
    """
    def repl(m):
        alt, url = m.group(1), m.group(2)
        url = unquote(url)
        base = os.path.basename(url)  # 去任何目录前缀，落到持久 media 根
        return "![%s](%s/%s)" % (alt, media_ref, base)
    return re.sub(r"!\[([^\]]*)\]\(([^)]*)\)", repl, markdown)


def usage(script: str) -> None:
    sys.stderr.write("usage: python3 %s <input_abs> <out_json_abs> <media_dir_abs>\n" % script)
    sys.exit(2)
