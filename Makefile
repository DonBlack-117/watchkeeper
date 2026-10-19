.PHONY: db run test test-short lint seed tidy

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
