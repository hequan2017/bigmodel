# 优先使用 ~/devops/toolchain/current 下的 Go 工具链（定时开发环境），否则用系统 go。
GO := $(shell if [ -x "$$HOME/devops/toolchain/current/bin/go" ]; then echo "$$HOME/devops/toolchain/current/bin/go"; else echo go; fi)

.PHONY: build run test vet up down

build:
	$(GO) build -o bin/server ./cmd/server

run:
	$(GO) run ./cmd/server

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

up:
	docker compose up -d db

down:
	docker compose down
