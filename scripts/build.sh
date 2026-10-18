#!/usr/bin/env bash
#
# 构建 gag 二进制。
#
#   ./scripts/build.sh                  # 只构建本机当前平台，产物在 dist/
#   ./scripts/build.sh -a               # 交叉编译 linux/amd64 + linux/arm64 + darwin/arm64
#   ./scripts/build.sh -c               # 构建前跑 gofmt/vet/test
#   ./scripts/build.sh -o /tmp/out      # 指定输出目录
#
# 为什么 CGO_ENABLED=0：产物要能直接塞进 distroless/static（见 build/Dockerfile），
# 也不希望 glibc 版本成为部署约束。本项目没有依赖 cgo 的库。

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

PKG=./cmd/main
BIN=gag
OUT=dist
ALL_PLATFORMS=0
CHECK=0

while getopts "o:ach" opt; do
	case "$opt" in
	o) OUT=$OPTARG ;;
	a) ALL_PLATFORMS=1 ;;
	c) CHECK=1 ;;
	h)
		sed -n '3,12p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "用法: $0 [-o 输出目录] [-a] [-c]" >&2
		exit 2
		;;
	esac
done

# 版本信息只写进日志与 /admin/status 的构建标识，不参与任何业务判断。
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_TIME=${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}

# 构建期要注入的变量统一在这里加，避免 -ldflags 散落在各处。
# 变量声明在 internal/gateway/build.go——注意 -X 必须写**完整导入路径**，
# 写成 main.version 会静默不生效（-X 找不到目标时只是不赋值，不报错）。
BUILD_PKG=gag/internal/gateway
LDFLAGS="-s -w -X ${BUILD_PKG}.version=${VERSION} -X ${BUILD_PKG}.commit=${COMMIT} -X ${BUILD_PKG}.buildTime=${BUILD_TIME}"

if [ "$CHECK" = "1" ]; then
	echo "==> gofmt"
	unformatted=$(gofmt -l cmd internal pkg 2>/dev/null || true)
	if [ -n "$unformatted" ]; then
		echo "以下文件未格式化：" >&2
		echo "$unformatted" >&2
		exit 1
	fi

	echo "==> go vet"
	go vet ./...

	echo "==> go test"
	go test ./...
fi

mkdir -p "$OUT"

build_one() {
	local goos=$1 goarch=$2 suffix=$3
	local target="$OUT/${BIN}${suffix}"
	echo "==> build ${goos}/${goarch} -> ${target}"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "$target" "$PKG"
}

if [ "$ALL_PLATFORMS" = "1" ]; then
	build_one linux amd64 ""
	build_one linux arm64 ""
	build_one darwin arm64 "-darwin-arm64"
else
	build_one "$(go env GOOS)" "$(go env GOARCH)" ""
fi

echo
echo "构建完成: ${VERSION} (${COMMIT})"
ls -lh "$OUT"
