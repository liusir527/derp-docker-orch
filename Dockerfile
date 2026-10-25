# Multi-stage: 国内镜像构建 platformd,运行时只装 docker-cli。
# 解决官方镜像在境内网络拉取慢/超时问题。

# ---- 构建阶段 ----
FROM docker.io/library/golang:1.25-alpine AS build
ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/platformd ./cmd/platformd \
    && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/regiond ./cmd/regiond

# ---- 运行时阶段 ----
FROM docker.io/library/alpine:3.22
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates docker-cli tzdata
COPY --from=build /out/platformd /usr/local/bin/platformd
COPY --from=build /out/regiond /usr/local/bin/regiond
# 以 root 跑:需要写 /data(orch-data 卷默认 root)与 BT stream 目录。
# docker.sock 已挂载,容器隔离主要靠网络而非 user。
# platformd 默认入口;地区宿主用 deploy/region-compose.yml 覆盖为 regiond。
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/platformd"]
