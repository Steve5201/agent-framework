#!/bin/sh
# 首次初始化 postgres 数据卷时自动创建各服务的独立数据库（P2-F 修复）。
#
# 说明：
#   - 每个后端服务使用独立库（auth / agent / llm），但官方镜像仅自动建
#     POSTGRES_DB（默认 postgres）一个库；后端没有"自动建库"逻辑，
#     因此必须在此补齐，否则服务启动会报 database does not exist。
#   - 该目录只在数据卷【首次】初始化时执行；对已存在的数据卷无效
#     （已有环境可手动 CREATE DATABASE 或删除卷重建）。
#   - 后续新增服务（如 knowledge/rag）在此追加一行。
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" <<-EOSQL
    CREATE DATABASE auth;
    CREATE DATABASE agent;
    CREATE DATABASE llm;
    CREATE DATABASE rag;
EOSQL
