FROM golang:1.27 AS build
WORKDIR /src

# 先只拷贝依赖清单：代码改动不会让 go mod download 缓存失效。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/gag ./cmd/main

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gag /usr/local/bin/gag
COPY configs/gateway.yaml /etc/gag/gateway.yaml

# 8080 业务端口，9090 管理端口
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/gag"]
CMD ["-config", "/etc/gag/gateway.yaml"]
