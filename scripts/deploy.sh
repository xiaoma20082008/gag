#!/usr/bin/env bash
#
# 部署脚本
#
#   ./scripts/deploy.sh start       # 启动网关容器
#   ./scripts/deploy.sh shutdown    # 关闭网关容器
#   ./scripts/deploy.sh restart     # 重建网关容器
#   ./scripts/deploy.sh logs        # 跟踪网关日志
#   ./scripts/deploy.sh status      # 容器与 /readyz 状态
#   ./scripts/deploy.sh image       # 只构建镜像，不启动
#   ./scripts/deploy.sh binary      # 只构建二进制（等同 scripts/build.sh -c）
#
# 环境变量：
#   IMAGE  镜像名，默认 gag:local
#   CONFIG 容器内配置路径，默认 /etc/gag/gateway.yaml

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

IMAGE=${IMAGE:-gag:local}
CONFIG=${CONFIG:-/etc/gag/gateway.yaml}
COMPOSE_FILE=build/docker-compose.yaml
SERVICE=gag

compose() { docker compose -f "$COMPOSE_FILE" "$@"; }

require_docker() {
	if ! command -v docker >/dev/null 2>&1; then
		echo "未找到 docker，请先安装 Docker Desktop 或 docker engine。" >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "docker daemon 未运行。" >&2
		exit 1
	fi
}

build_image() {
	require_docker
	echo "==> 构建镜像 ${IMAGE}"
	# 构建上下文是仓库根目录：Dockerfile 需要 go.mod/go.sum 与全部源码。
	docker build -f build/Dockerfile -t "$IMAGE" .
}

wait_ready() {
	local url=${1:-http://127.0.0.1:9090/readyz}
	echo "==> 等待 ${url}"
	for _ in $(seq 1 30); do
		if curl -fsS --noproxy '*' "$url" >/dev/null 2>&1; then
			echo "就绪: $(curl -fsS --noproxy '*' "$url")"
			return 0
		fi
		sleep 1
	done
	echo "等待就绪超时，请查看日志: $0 logs" >&2
	return 1
}

case "${1:-}" in
start)
	require_docker
	build_image
	echo
	echo "!! 提示：compose 里网关服务默认是注释掉的，原因见 ${COMPOSE_FILE} 文件头。"
	echo "!! 若已确认配置中的实例地址能在容器网络内解析，请先放开该服务再执行。"
	echo
	compose start -d nacos redis mysql
	wait_ready http://127.0.0.1:8848/nacos/ || true
	echo "依赖已启动。宿主机上运行网关："
	echo "  go run ./cmd/main -c configs/gateway.yaml"
	;;
shutdown)
	require_docker
	compose shutdown
	;;
restart)
	require_docker
	build_image
	compose start -d --no-deps --force-recreate "$SERVICE"
	;;
logs)
	require_docker
	compose logs -f --tail=200 "$SERVICE"
	;;
status)
	require_docker
	compose ps
	echo
	echo "==> /readyz"
	curl -fsS --noproxy '*' http://127.0.0.1:9090/readyz || echo "（数据面未就绪或未启动）"
	echo
	echo "==> /admin/status"
	# admin_token 从配置里读，避免把 token 写进脚本。
	token=$(awk '/admin_token:/ {gsub(/"/, "", $2); print $2; exit}' configs/gateway.yaml || true)
	curl -fsS --noproxy '*' -H "Authorization: Bearer ${token}" \
		http://127.0.0.1:9090/admin/status || echo "（管理接口不可达）"
	echo
	;;
image)
	build_image
	;;
binary)
	./scripts/build.sh -c
	;;
*)
	sed -n '3,20p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
