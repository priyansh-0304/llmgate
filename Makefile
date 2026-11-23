SHELL := /bin/bash
.DEFAULT_GOAL := help
NS := llmgate

.PHONY: help tidy test run-mock run-gateway env secrets cluster images load deploy up down status loadtest-mock loadtest-gateway unlimit limit compose-up compose-down dashboard-dev

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

## ---------- Go, no Docker needed ----------
tidy: ## Download Go dependencies and write go.sum (run once)
	go mod tidy

test: ## Run all Go tests
	go test ./...

run-mock: ## Run the mock upstream on :9000
	go run ./cmd/mock-upstream

run-gateway: ## Run the gateway on :8080 against the mock (in-memory limiter/cache)
	DEFAULT_ROUTE=mock MOCK_URL=http://localhost:9000 go run ./cmd/gateway

## ---------- secrets ----------
env: ## Generate random local secrets (.env and k8s secret)
	./scripts/gen-secrets.sh
secrets: env

## ---------- kind (Kubernetes in Docker) ----------
cluster: ## Create the kind cluster
	kind create cluster --config deploy/kind-config.yaml

images: ## Build the three container images
	docker build -f deploy/Dockerfile.app --target gateway -t llmgate/gateway:dev .
	docker build -f deploy/Dockerfile.app --target mock-upstream -t llmgate/mock-upstream:dev .
	docker build -f dashboard/Dockerfile -t llmgate/dashboard:dev .

load: ## Load the images into the kind cluster
	kind load docker-image --name llmgate llmgate/gateway:dev llmgate/mock-upstream:dev llmgate/dashboard:dev

deploy: ## Apply namespace, secret and all manifests
	kubectl apply -f deploy/k8s/00-namespace.yaml
	kubectl apply -f deploy/k8s/secret.local.yaml
	kubectl apply -k deploy/k8s

up: secrets cluster images load deploy ## Everything, start to finish
	@echo; echo "Gateway http://localhost:8080  Guide http://localhost:3000  Dashboard http://localhost:3000/dashboard/  Grafana http://localhost:3001  Prometheus http://localhost:9090"

down: ## Delete the kind cluster
	kind delete cluster --name llmgate

status: ## Show pods
	kubectl -n $(NS) get pods

## ---------- load tests (k6) ----------
loadtest-mock: ## Baseline: hit the mock upstream directly (no limiter, no 429)
	k6 run -e TARGET=http://localhost:9000 loadtest/k6.js

loadtest-gateway: ## Through the gateway (run `make unlimit` first)
	k6 run -e TARGET=http://localhost:8080 -e MODEL=mock-fast -e API_KEY=$$(grep ^GATEWAY_API_KEY .env | cut -d= -f2) loadtest/k6.js

unlimit: ## Raise the gateway rate limit so load tests measure latency, not 429s
	kubectl -n $(NS) patch configmap gateway-config --type merge -p '{"data":{"RATE_CAPACITY":"100000","RATE_REFILL_PER_SEC":"100000","DEFAULT_ROUTE":"mock"}}'
	kubectl -n $(NS) rollout restart deploy/gateway

limit: ## Restore the default rate limit and route
	kubectl -n $(NS) patch configmap gateway-config --type merge -p '{"data":{"RATE_CAPACITY":"20","RATE_REFILL_PER_SEC":"10","DEFAULT_ROUTE":"ollama"}}'
	kubectl -n $(NS) rollout restart deploy/gateway

## ---------- docker compose ----------
compose-up: env ## Run everything with docker compose (mock upstream)
	docker compose up --build
compose-down: ## Stop docker compose
	docker compose down

dashboard-dev: ## Dashboard dev server on :5173/dashboard/ (proxies /api to :8080; falls back to demo data)
	cd dashboard && npm run dev
