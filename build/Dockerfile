# 多阶段构建：编译期用完整工具链，运行期只留一个静态二进制。
#
# 运行镜像选 distroless/static：网关不写磁盘、不需要 shell，
# 镜像越小攻击面越小；需要排查时用 `kubectl debug` 挂临时容器。

FROM golang:1.27 AS build
WORKDIR /src

# 先只拷贝依赖清单：代码改动不会让 go mod download 缓存失效。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 版本信息由 CI 传入（docker build --build-arg VERSION=...）；
# 未传时落到 dev，二进制仍然可用，只是 /admin/status 里看不出是哪个构建。
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/gag ./cmd/main

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gag /usr/local/bin/gag
COPY configs/gateway.yaml /etc/gag/gateway.yaml

# 8080 业务端口，9090 管理端口（生产环境管理端口通常只在内网暴露）
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/gag"]
CMD ["-config", "/etc/gag/gateway.yaml"]
