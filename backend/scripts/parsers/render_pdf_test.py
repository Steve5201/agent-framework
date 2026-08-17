# -*- coding: utf-8 -*-
"""render_pdf 打印增强回归测试（P5-HTML PDF 导出）。

运行方式（复用 .smoke_pydeps 本地依赖）：
    python scripts/parsers/render_pdf_test.py

覆盖：
1. extract_title 从 <title>/首个 <h1> 提取页眉标题，兜底"智能文档"
2. inject_print_css 打印增强 CSS 注入（</head> → </body> → 末尾兜底）
3. wrap_body_with_header_footer 把 <body> 内容包进 table.doc-shell
   （页眉/页脚每页重复），标题转义、无 <body> 时不包裹
"""
import os
import sys
import unittest

_PARSERS = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, _PARSERS)
sys.path.insert(0, os.path.join(_PARSERS, ".smoke_pydeps"))

import render_pdf as rp  # noqa: E402


class ExtractTitleTest(unittest.TestCase):
    def test_from_title_tag(self):
        html = "<html><head><title>教案：高等数学</title></head><body><h1>正文标题</h1></body></html>"
        self.assertEqual(rp.extract_title(html), "教案：高等数学")

    def test_fallback_to_h1(self):
        html = "<html><head></head><body><h1>示例大学 · 智能助手</h1></body></html>"
        self.assertEqual(rp.extract_title(html), "示例大学 · 智能助手")

    def test_h1_with_nested_tags(self):
        html = "<html><head></head><body><h1><span>第一章</span> 绪论</h1></body></html>"
        self.assertEqual(rp.extract_title(html), "第一章 绪论")

    def test_empty_title_then_h1(self):
        html = "<html><head><title>   </title></head><body><h1>真实标题</h1></body></html>"
        self.assertEqual(rp.extract_title(html), "真实标题")

    def test_no_title_no_h1(self):
        self.assertEqual(rp.extract_title("<html><body><p>x</p></body></html>"), "智能文档")

    def test_title_truncated_to_60(self):
        html = "<html><head><title>%s</title></head></html>" % ("长" * 100)
        self.assertEqual(rp.extract_title(html), "长" * 60)


class InjectPrintCssTest(unittest.TestCase):
    def test_injected_into_head(self):
        html = "<html><head><style>p{}</style></head><body>x</body></html>"
        out = rp.inject_print_css(html)
        self.assertIn('font-family: "Noto Sans CJK SC"', out)
        self.assertIn("print-color-adjust: exact", out)
        # 注入在 </head> 之前（用户样式之后），且只注入一份。
        self.assertEqual(out.count("table.doc-shell {"), 1)
        self.assertTrue(out.index("table.doc-shell {") > out.index("p{}"))

    def test_fallback_body_when_no_head(self):
        html = "<html><body>x</body></html>"
        out = rp.inject_print_css(html)
        self.assertIn("print-color-adjust: exact", out)
        self.assertIn("<style>", out)

    def test_append_when_no_head_no_body(self):
        html = "plain text"
        out = rp.inject_print_css(html)
        self.assertIn("print-color-adjust: exact", out)
        self.assertTrue(out.startswith("plain text"))


class WrapBodyWithHeaderFooterTest(unittest.TestCase):
    def test_wraps_body_content(self):
        html = "<html><head></head><body><p>正文</p></body></html>"
        out = rp.wrap_body_with_header_footer(html, "测试文档")
        self.assertIn('<table class="doc-shell">', out)
        self.assertIn("<thead><tr><td>测试文档</td></tr></thead>", out)
        self.assertIn("智能助手生成 · 内部资料", out)
        # 原文在 tbody 单元格内，未被破坏。
        self.assertIn("<tbody><tr><td>\n<p>正文</p>\n  </td></tr></tbody>", out)
        # 一个 body 只出现一次。
        self.assertEqual(out.count("<body>"), 1)

    def test_title_html_escaped(self):
        html = "<html><body>内容</body></html>"
        out = rp.wrap_body_with_header_footer(html, '<"&>')
        self.assertIn("<td>&lt;&quot;&amp;&gt;</td>", out)

    def test_no_body_returns_unchanged(self):
        html = "<html><head></head></html>"
        self.assertEqual(rp.wrap_body_with_header_footer(html, "t"), html)

    def test_missing_closing_body(self):
        html = "<html><body><p>没有闭合</p>"
        out = rp.wrap_body_with_header_footer(html, "t")
        self.assertIn("</table>", out)
        self.assertTrue(out.endswith("</table>\n"))


if __name__ == "__main__":
    unittest.main()
