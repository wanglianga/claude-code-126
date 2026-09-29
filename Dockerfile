# ---- 构建阶段 ----
FROM golang:1.19-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /coffee-server ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=builder /coffee-server /usr/local/bin/coffee-server
ENV TZ=Asia/Shanghai
USER app
EXPOSE 8080
ENTRYPOINT ["coffee-server"]
