# 多阶段构建：编译 + 最小运行镜像。
# 构建参数 GO_IMAGE 用于在 Docker Hub 不可达时切换镜像源，例如：
#   docker build --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.27-alpine -t bigmodel-app .
ARG GO_IMAGE=golang:1.27-alpine
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
    && CGO_ENABLED=0 go build -trimpath -o /out/seed ./cmd/seed

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/server /app/server
COPY --from=build /out/seed /app/seed
# 文档原始文件目录：单实例本地卷；多副本部署需替换为共享存储（§16.2）。
VOLUME ["/data"]
ENV APP_HTTP_ADDR=:8080 \
    APP_DATA_DIR=/data \
    APP_LOG_LEVEL=info
EXPOSE 8080
ENTRYPOINT ["/app/server"]
