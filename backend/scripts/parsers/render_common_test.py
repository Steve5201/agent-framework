# -*- coding: utf-8 -*-
"""render_common 回归测试（P4-L 公式渲染）。

运行方式（复用 .smoke_pydeps 本地依赖）：
    python scripts/parsers/render_common_test.py

覆盖：
1. clean_latex 中文清理（公式中文乱码修复：LLM 输出带中文说明前缀时剥离）
2. latex_to_omml 原生公式（OMML）——含回归：早期实现 _parse_atom 不推进
   token 位置导致 \frac/\sum/x^2 等解析死循环的 bug。
3. 空/纯中文输入安全报错。
"""
import os
import sys
import unittest

_PARSERS = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, _PARSERS)
sys.path.insert(0, os.path.join(_PARSERS, ".smoke_pydeps"))

import render_common as rc  # noqa: E402


class CleanLatexTest(unittest.TestCase):
    def test_strip_chinese_prefix(self):
        # LLM 常输出 "LaTeX 公式：$...$"/"公式为 $...$" 等带中文说明的文本。
        cases = [
            ("LaTeX 公式：$x=\\frac{a}{b}$", r"x=\frac{a}{b}"),
            ("$$\\sum_{i=1}^{n} i$$", r"\sum_{i=1}^{n} i"),
            ("公式为 $E=mc^2$，其中 E 为能量", "E=mc^2"),
            ("$a^2 + b^2 = c^2$", "a^2 + b^2 = c^2"),
        ]
        for raw, want in cases:
            self.assertEqual(rc.clean_latex(raw), want, "raw=%r" % raw)

    def test_pure_chinese_or_empty(self):
        self.assertEqual(rc.clean_latex("这是一个公式"), "")
        self.assertEqual(rc.clean_latex("  "), "")


class LatexToOmmlTest(unittest.TestCase):
    def test_frac(self):
        omml = rc.latex_to_omml(r"\frac{a}{b}")
        self.assertTrue(omml.startswith("<m:oMath"), omml)
        self.assertIn("<m:f>", omml)
        self.assertIn("<m:num>", omml)
        self.assertIn("<m:den>", omml)

    def test_superscript(self):
        # 回归：单字符原子必须推进位置，^ 才能附着成 sSup（曾死循环）。
        omml = rc.latex_to_omml("x^2")
        self.assertIn("<m:sSup>", omml)
        self.assertIn("<m:sup>", omml)

    def test_nary_sub_sup(self):
        omml = rc.latex_to_omml(r"\sum_{i=1}^{n} i")
        self.assertIn("<m:nary>", omml)
        self.assertIn("<m:sub>", omml)
        self.assertIn("<m:sup>", omml)

    def test_unknown_command_skipped(self):
        # 回归：未知命令（\, \quad 等）不推进位置曾导致死循环；现安全跳过。
        omml = rc.latex_to_omml(r"a \cdot b")
        self.assertIn("<m:r>", omml)

    def test_empty_raises(self):
        with self.assertRaises(ValueError):
            rc.latex_to_omml("中文公式")
        with self.assertRaises(ValueError):
            rc.latex_to_omml("  ")


if __name__ == "__main__":
    unittest.main(verbosity=2)
