# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
# 国内构建加速开关：默认启用。
# 背景：update.sh 依赖本文件解决「国内直接拉 proxy.golang.org / dl-cdn.alpinelinux.org 超时」。
# 国外或自建镜像环境构建时传 --build-arg CN_MIRROR=0 关闭，改走官方源。
ARG CN_MIRROR=1
WORKDIR /src
COPY go.mod ./
# ARG 在同一 stage 内对后续所有 RUN 可见，无需重复声明。
RUN if [ "$CN_MIRROR" = "1" ]; then export GOPROXY=https://goproxy.cn,direct; fi; go mod download
COPY . .
# 一次编译全部二进制（工具进镜像，容器内可直接跑脚本）。全部 -trimpath -s -w。
RUN if [ "$CN_MIRROR" = "1" ]; then export GOPROXY=https://goproxy.cn,direct; fi; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wb2api ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/signin_bin ./cmd/signin \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/login ./cmd/login \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/credit ./cmd/credit

FROM alpine:3.20
# runtime 阶段同样支持国内加速（apk 安装 wget/python3/bash 等）。
# 注意 ARG 作用域只在单个 FROM 阶段内，这里必须重新声明。
ARG CN_MIRROR=1
# python3：login.sh 的 JSON 解析 / 签到 / 落盘；bash：shell 脚本体。
RUN if [ "$CN_MIRROR" = "1" ]; then \
      sed -i 's#dl-cdn.alpinelinux.org#mirrors.aliyun.com#g' /etc/apk/repositories; \
    fi \
 && apk add --no-cache wget ca-certificates tzdata python3 bash \
 && adduser -D -u 10001 app \
 && mkdir -p /app/auths /app/data \
 && chown -R app:app /app
WORKDIR /app
# 脚本置入 + 去 CRLF（Windows 检出可能性）在切到 app 之前以 root 完成——
# app 对 root 所有文件无写权限，sed -i 需要写权限。
COPY --from=build /out/wb2api /app/wb2api
COPY --from=build /out/signin_bin /app/signin_bin
COPY --from=build /out/login /app/login
COPY --from=build /out/credit /app/credit
COPY login.sh signin.sh credit.sh /app/
COPY scripts/probe_active.py /app/scripts/probe_active.py
RUN sed -i 's/\r$//' /app/login.sh /app/signin.sh /app/credit.sh && chmod 755 /app/login.sh /app/signin.sh /app/credit.sh
# 镜像不带真实配置：落 example 作为默认（生产由挂载卷 /app/config.json 覆盖）
COPY config.example.json /app/config.json
USER app
EXPOSE 7863
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:7863/healthz || exit 1
ENTRYPOINT ["/app/wb2api", "-config", "/app/config.json"]