# parse_pdf.py —— PDF 解析（PyMuPDF）。
#
# 能力：
#   - 文本提取（含标题启发式：页首大字号 span 视为章节标题）
#   - 图片提取（保存 PNG 到媒体目录，markdown 插入占位引用）
#   - 扫描版检测（无文本层 → scan_only + warning）
#
# 已知取舍（P3-A3b 已确认）：PDF 内公式是矢量图形/碎片文本，文本层提取不到
# LaTeX，本期不接视觉模型兜底；扫描版提示用户转 Word/可选中文字的 PDF。
#
# usage: python3 parse_pdf.py <input_abs> <out_json_abs> <media_dir_abs>
import sys

import common


def main() -> int:
    if len(sys.argv) != 4:
        common.usage("parse_pdf.py")
    input_path, out_path, media_dir = sys.argv[1], sys.argv[2], sys.argv[3]
    media_ref = common.media_rel_prefix(media_dir)

    try:
        import fitz  # PyMuPDF
    except ImportError:
        common.emit(out_path, "", "", [], False,
                    ["沙盒缺少 PyMuPDF（镜像构建期未安装 pymupdf），无法解析 PDF"])
        return 0

    warnings: list[str] = []
    media: list[dict] = []
    try:
        doc = fitz.open(input_path)
    except Exception as e:  # noqa: BLE001
        common.emit(out_path, "", "", [], False, ["PDF 打开失败: %s" % e])
        return 0

    page_count = doc.page_count
    text_pages = 0
    md: list[str] = []  # 按页序收集 markdown 行

    for pno in range(page_count):
        page = doc[pno]
        d = page.get_text("dict")
        blocks = d.get("blocks", []) if d else []

        # 页内最大字号：用于标题启发式（页首且接近最大字号的 span → 标题）。
        max_size = 0.0
        for b in blocks:
            if b.get("type") != 0:
                continue
            for line in b.get("lines", []):
                for span in line.get("spans", []):
                    if span["size"] > max_size:
                        max_size = span["size"]

        page_chars = 0
        top_zone = page.rect.height * 0.18
        for b in blocks:
            if b.get("type") != 0:
                continue
            bbox = b.get("bbox", [0.0, 0.0, 0.0, 0.0])
            near_top = bbox[1] < top_zone
            for line in b.get("lines", []):
                for span in line.get("spans", []):
                    text = (span.get("text") or "").strip()
                    if not text:
                        continue
                    page_chars += len(text)
                    if near_top and max_size > 0 and span["size"] >= max_size * 0.92:
                        md.append("## " + text)
                    else:
                        md.append(text)
        if page_chars >= 50:
            text_pages += 1

        # 图片提取（与文本同页，追加在页尾）。
        try:
            for i, img in enumerate(page.get_images(full=True)):
                try:
                    xref = img[0]
                    pix = fitz.Pixmap(doc, xref)
                    if pix.n - pix.alpha > 3:  # CMYK 等 → RGB
                        pix = fitz.Pixmap(fitz.csRGB, pix)
                    ref = common.save_bytes(
                        pix.tobytes("png"), media_dir, "fig_p%d_%d.png" % (pno + 1, i), media_ref)
                    alt = "图 %d-%d" % (pno + 1, i + 1)
                    media.append({"type": "image", "path": ref, "alt": alt})
                    md.append("![%s](%s)" % (alt, ref))
                except Exception:  # 单张图片失败不影响整篇
                    continue
        except Exception:  # get_images 异常时跳过图片提取
            pass

    doc.close()

    scan_only = False
    if page_count > 0 and text_pages < max(1, int(page_count * 0.3)):
        scan_only = True
        warnings.append(
            "检测到扫描版 PDF（无可选中文本层），正文无法提取；"
            "建议上传可选中文字的 PDF 或 Word/PPT 源文件")

    title = ""
    for line in md:
        if line.startswith("## "):
            title = line[3:].strip()
            break

    common.emit(out_path, title, "\n\n".join(md), media, scan_only, warnings)
    return 0


if __name__ == "__main__":
    sys.exit(main())
