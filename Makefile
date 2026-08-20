VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: help build run test test-short test-race test-integration vet fmt tidy genkey docker-up docker-down clean

help:
	@echo "build            compila o binário em ./bin"
	@echo "run              sobe o servidor com o ambiente atual"
	@echo "test             roda todos os testes (integração usa Postgres embutido)"
	@echo "test-short       roda só os testes unitários (pula integração)"
	@echo "test-race        roda os testes com o detector de corrida (exige compilador C)"
	@echo "vet              go vet"
	@echo "genkey           gera uma VODM_ENCRYPTION_KEY"
	@echo "docker-up        sobe app + postgres via compose"

build:
	go build -ldflags "$(LDFLAGS)" -o bin/vodmanager ./cmd/vodmanager

run:
	go run -ldflags "$(LDFLAGS)" ./cmd/vodmanager

test:
	go test ./...

test-short:
	go test -short ./...

# Precisa de um compilador C (gcc/clang). No Windows: mingw-w64. No CI (Linux) já existe.
test-race:
	CGO_ENABLED=1 go test -race ./...

test-integration:
	go test -v ./test/integration/

vet:
	go vet ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

genkey:
	go run ./cmd/vodmanager genkey

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

clean:
	rm -rf bin
