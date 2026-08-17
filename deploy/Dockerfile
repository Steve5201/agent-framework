# 统一构建脚本：通过 --build-arg SERVICE 选择要编译的服务。
#
#   docker build -f deploy/Dockerfile --build-arg SERVICE=auth -t agent/auth .
#
# 可用 SERVICE：auth | agent | llm-gateway | gateway（对应 backend/cmd/<SERVICE>）。
#
# 设计说明：
#   - 多阶段构建：builder 阶段编译出静态二进制，runtime 阶段只装二进制
#     （CGO_ENABLED=0 无 glibc 依赖，alpine 可直接运行）；
#   - monorepo 兼容：backend 的 go.mod 用 replace 指向 ../framework，
#     因此两个 module 都要拷进构建上下文。
#   - 不用 `# syntax=docker/dockerfile:1`：该指令会让 BuildKit 额外拉取
#     docker/dockerfile 前端镜像（国内访问 auth.docker.io 常被拒）；省略后
#     BuildKit 使用内置前端，行为等价。
ARG SERVICE=gateway

# ---- 编译阶段 ----
FROM golang:1.26-alpine AS builder
ARG SERVICE

# ca-certificates 供调用方走 HTTPS（llm-gateway 调 DeepSeek 需要）。
# 先换国内 apk 源（dl-cdn.alpinelinux.org 在国内常超时，导致构建卡死）。
RUN sed -i 's#dl-cdn.alpinelinux.org#mirrors.aliyun.com#g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates

# Go 模块代理：默认 proxy.golang.org 在国内常被拒（connection refused），
# 改用国内镜像，且配置多源——单源连接中断（unexpected EOF，国内镜像偶发）时
# Go 会自动 fallback 到下一个代理；direct 兜底私有/未镜像模块直连源站。
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,direct

WORKDIR /src
# 先拷贝两个 module 的全部源码（backend 内嵌迁移与文档资源）。
COPY backend/ backend/
COPY framework/ framework/

WORKDIR /src/backend
# 预下载依赖（利用构建缓存，源码不变时依赖层不重建）。
# 国内代理偶发"unexpected EOF"（连接中途被掐断），重试两次兜底（多源已在
# GOPROXY 层覆盖，但整层网络抖动时仍需重跑），降低构建失败率。
RUN go mod download || (sleep 5 && go mod download) || (sleep 15 && go mod download)

# 静态编译 + 裁剪符号表。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/app ./cmd/${SERVICE}

# ---- 运行阶段 ----
FROM alpine:3.20
ARG SERVICE
# 服务默认以非 root 的 app 用户运行；sandbox 服务需要 root 权限来执行
# unshare（新建网络命名空间）/ setpriv（降权），compose 构建时传 RUN_AS=root。
ARG RUN_AS=app

# 非 root 运行（安全基线）；ca-certificates 为上游 HTTPS 调用；
# python3 供内置工具 code_executor 的 python 解释器（内置 sh 由 busybox 提供）。
# /app 为智能体工作目录：file_ops / code_executor / /files 媒体服务的统一根目录，
# 属主为 app 用户，保证写操作可落盘。
# /logs 为日志落盘目录（compose 挂载 logs 卷）：预建并改属主，否则容器内
# 非 root 的 app 用户无写权限，logger 启动即 panic（exit 2）。
# app 用户固定 uid/gid 100：与宿主机 Docker Desktop 挂载卷的权限映射对齐
# （实测 uid 100 可写 bind mount，uid 1000 会 permission denied）。file_ops 以
# app(100) 写 /app/users/<uid>；sandbox 容器（root 主进程）用 setfacl 对每个
# 用户工作区授予 u:100:rwx ACL，保证 agent 仍可读写共享数据卷，而沙盒执行
# 进程以"每用户独立 uid"（SANDBOX_UID_BASE+user_id）运行，用户间强隔离。
RUN sed -i 's#dl-cdn.alpinelinux.org#mirrors.aliyun.com#g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates python3 tzdata && \
    addgroup -S app && adduser -S -G app app && \
    mkdir -p /app /logs && chown app:app /app /logs
# sandbox 服务专属（阶段2·容器沙盒 + 每用户独立 uid 隔离）：
#   - util-linux：unshare（禁网）/ prlimit（资源限制）/ setpriv（降权）；
#   - acl：setfacl，对每个用户工作区递归授予 agent 用户（uid 100）读写 ACL，
#     实现"沙盒用户之间按独立 uid 强隔离，agent 仍可协作读写"；
#   - 执行进程降权到"该用户专属 uid"（SANDBOX_UID_BASE + user_id，uid==gid），
#     目录属主即该 uid + other=0，用户 A 的脚本读不到用户 B 的目录；
#   - /work 工作区根（compose 挂载数据卷，容器内由 root 主进程按用户建子目录）。
# 其它服务不安装 util-linux/acl，保持镜像精简。
# P3-A3b 起 sandbox 兼作「文档解析沙盒」（pdf/docx/pptx 委托解析）：
#   - pandoc：docx→markdown（OMML 公式→$LaTeX$，教育场景刚需）；
#   - pymupdf / python-docx / python-pptx：PDF 文本+图片提取、docx/pptx 降级与媒体提取；
#   - py3-matplotlib（系统包，P4-J 阶段2）：文档生成公式块（LaTeX mathtext → 透明 PNG）；
#   - chromium + font-noto-cjk + nss_wrapper（P5-HTML HTML 中间层文档生成的 PDF
#     导出）：Chromium headless 官方打印管线 `--headless --print-to-pdf`（业界
#     标准）。Alpine 是 musl 构建，playwright 等 Python 库无 musllinux wheel 无法
#     pip 安装，故直接用系统 chromium 二进制（render_pdf.py 内置查找，零 Python
#     渲染依赖）；font-noto-cjk 保证中文渲染不缺字体（缺失会显示方框）；
#     nss_wrapper 解决降权派生 uid（2000+user_id）无 /etc/passwd 条目时
#     chromium/crashpad 无法解析 home 路径而崩溃的问题（业界标准解法）；
#   - chromium-swiftshader：Alpine 把 SwiftShader（CPU 实现的 Vulkan，ANGLE
#     SwANGLE 后端）拆成独立子包。只装 chromium 时 GPU 进程 EGL 全部初始化失败
#     （EGL_NOT_INITIALIZED + "Printing failed"，--print-to-pdf 产出为空），且
#     chromium 自带的 libvulkan.so.1 只认模块目录下的 vk_swiftshader_icd.json
#     （/usr/lib/chromium/./vk_swiftshader_icd.json），VK_ICD_FILENAMES 无效——
#     所以必须在构建期装包把 icd json + libvk_swiftshader.so 落到 /usr/lib/chromium/。
#   - 沙盒执行进程禁网（unshare -n），这些库必须在镜像构建期装好，运行时无法安装。
RUN if [ "$SERVICE" = "sandbox" ]; then \
      apk add --no-cache util-linux acl py3-pip pandoc py3-matplotlib chromium chromium-swiftshader font-noto-cjk font-wqy-zenhei nss_wrapper && \
      pip install --break-system-packages -i https://mirrors.aliyun.com/pypi/simple/ --extra-index-url https://pypi.tuna.tsinghua.edu.cn/simple \
        "pymupdf==1.24.9" "python-docx==1.1.2" "python-pptx==1.0.2" && \
      mkdir -p /work && chown app:app /work && \
      mkdir -p /opt/mplconfig && chmod 777 /opt/mplconfig && \
      # matplotlib 中文字体全局配置（PNG 里的中文标签乱码根因）：
      #   - font-noto-cjk 提供的是 .ttc 集合，matplotlib 的 ft2font 只注册 face 0
      #     （实测仅 "Noto Sans CJK JP"，没有 SC/TC 变体名）；
      #   - font-wqy-zenhei（文泉驿正黑）是独立 OTF，简体字形全，matplotlib 可直接
      #     按 "WenQuanYi Zen Hei" 找到，优先级放最前；
      #   - 放在 /etc/matplotlibrc（matplotlib 全局配置搜索路径）而非 /opt/mplconfig
      #     卷内，镜像重建即生效、不受 mplconfig 卷 copy-up 残留影响。
      printf '%s\n' \
        'font.family: sans-serif' \
        'font.sans-serif: WenQuanYi Zen Hei, Noto Sans CJK JP, Noto Sans CJK KR, Noto Sans CJK TC, DejaVu Sans' \
        'axes.unicode_minus: False' \
        'figure.facecolor: white' \
        'savefig.facecolor: white' \
        > /etc/matplotlibrc && \
      MPLCONFIGDIR=/opt/mplconfig python3 -c "import matplotlib; matplotlib.use('Agg'); import matplotlib.font_manager as fm; fm._load_fontmanager(try_read_cache=False); p=fm.findfont('WenQuanYi Zen Hei', fallback_to_default=False); print('CJK font:', p)" && \
      echo "matplotlib font cache primed"; \
    fi

# 本地 MCP（stdio）运行环境（agent / gateway 专属）：
#   - agent 实际拉起 MCP 子进程；gateway 管理端的"测试连接/启用"也会拉起子进程，
#     两个容器都必须能 import 上传的 Python MCP 依赖；
#   - 预装 mcp SDK 并固定 1.x：mcp 2.x 已移除 mcp.server.fastmcp 模块
#     （FastMCP 独立成包），而社区演示包（如 testdata/calculator-mcp）仍用
#     `from mcp.server.fastmcp import FastMCP`；
#   - --break-system-packages：alpine 3.20 的 pip 受 PEP 668 约束，容器环境
#     每次从镜像重建、内容即官方包集，允许全局安装（venv 反而不便 MCP 复用）。
#   - PyPI 源：主源阿里云（实测稳定），extra 兜底清华（国内镜像偶发空索引）。
RUN if [ "$SERVICE" = "agent" ] || [ "$SERVICE" = "gateway" ]; then \
      apk add --no-cache py3-pip && \
      pip install --break-system-packages -i https://mirrors.aliyun.com/pypi/simple/ --extra-index-url https://pypi.tuna.tsinghua.edu.cn/simple mcp==1.29.0; \
    fi

WORKDIR /app

COPY --from=builder /out/app /usr/local/bin/app

# 预置解析脚本（P3-A3b）：pdf/docx/pptx 委托 sandbox 的解析器
# （profiles.go 注册 + rag 侧 sandboxclient 调用），与 Go 代码同仓版本管理。
# 仅 sandbox 使用；其它服务多拷一份镜像文件，体积可忽略。
COPY --from=builder /src/backend/scripts/parsers /opt/rag-parsers

USER ${RUN_AS}
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/app"]
