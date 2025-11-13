#!/bin/bash

# 测试擦除码客户端 SDK

set -e

echo "=== MinIO 擦除码客户端测试 ==="
echo ""

# 进入示例目录
cd "$(dirname "$0")"

# 检查 Go 环境
if ! command -v go &> /dev/null; then
    echo "错误: 需要安装 Go"
    exit 1
fi

# 下载依赖
echo "1. 下载依赖..."
go mod tidy

# 编译
echo ""
echo "2. 编译客户端..."
go build -o erasure-client main.go

# 检查 MinIO 服务器
echo ""
echo "3. 检查 MinIO 服务器..."
if ! curl -s http://localhost:9000/minio/health/live > /dev/null 2>&1; then
    echo "警告: MinIO 服务器未运行在 localhost:9000"
    echo "请先启动 MinIO 服务器:"
    echo "  ./minio server /data"
    echo ""
    echo "或者修改 main.go 中的 endpoint 配置"
    exit 1
fi

echo "✓ MinIO 服务器运行中"

# 运行测试
echo ""
echo "4. 运行测试..."
./erasure-client

echo ""
echo "=== 测试完成 ==="
