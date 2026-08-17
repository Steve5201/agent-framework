#!/bin/sh
# 演示脚本：code_executor 从技能目录执行（cwd 已在技能目录内）。
# 用法：sh scripts/hello.sh <名称>
name="${1:-世界}"
echo "你好，${name}！这是技能模板的演示输出。"
