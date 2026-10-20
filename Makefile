.PHONY: db run test test-short lint seed tidy web web-dev web-test build image

# Testcontainers with rootless Podman: use its socket when Docker is not there.
PODMAN_SOCK := /run/user/$(shell id -u)/podman/podman.sock
ifneq ($(wildcard $(PODMAN_SOCK)),)
  ifeq ($(DOCKER_HOST),)
    export DOCKER_HOST := unix://$(PODMAN_SOCK)
    export TESTCONTAINERS_RYUK_DISABLED := true
  endif
endif

db:
	podman compose up -d postgres || docker compose up -d postgres

# Local development: allow checks against localhost (the other projects run there).
run:
	WK_ALLOW_PRIVATE_TARGETS=true go run ./cmd/watchkeeper

test:
	go test -race ./...

test-short:
	go test -race -short ./...

lint:
	go vet ./...
	golangci-lint run

seed:
	./scripts/seed.sh

tidy:
	go mod tidy

# Frontend: build it once so `make run` serves the dashboard on :8090,
# or run `make web-dev` for Vite with hot reload on :5173 (proxies /api).
web:
	cd web && npm ci --no-audit --no-fund && npm run build

web-dev:
	cd web && npm run dev

web-test:
	cd web && npm run lint && npm run typecheck && npm test && npm run test:e2e

build: web
	CGO_ENABLED=0 go build -trimpath -o watchkeeper ./cmd/watchkeeper

image:
	podman build --format docker -t watchkeeper:dev . || docker build -t watchkeeper:dev .
