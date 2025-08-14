# syntax=docker/dockerfile:1

# -- Build stage --
FROM golang:1.24.5-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /main cmd/server/main.go

# -- Final stage -- 
FROM alpine:3

EXPOSE 8080 50051

COPY --from=builder main /bin/main
COPY --from=ghcr.io/grpc-ecosystem/grpc-health-probe:v0.4.40 /ko-app/grpc-health-probe /bin/grpc_health_probe

ENTRYPOINT [ "/bin/main" ]