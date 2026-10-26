GO ?= go
PROTOC ?= protoc
PYTHON ?= python3
.PHONY: generate test test-runtime test-mcp race build build-mcp lab clean-lab regression
generate:
	$(PROTOC) -I . --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/agent/v1/agent.proto
test:
	$(GO) test ./...
	$(PYTHON) -m unittest discover -s docker -p '*_test.py' -v
test-runtime:
	$(PYTHON) -m unittest discover -s docker -p '*_test.py' -v
test-mcp:
	$(GO) test -race ./mcp/...
race:
	$(GO) test -race -count=1 ./...
build: build-mcp
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -o bin/sdn-agent ./cmd/sdn-agent
	CGO_ENABLED=0 $(GO) build -o bin/sdnctl ./cmd/sdnctl
	CGO_ENABLED=0 $(GO) build -o bin/mock-controller ./cmd/mock-controller
build-mcp:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -o bin/sdn-agent-mcp ./mcp/cmd/sdn-agent-mcp
lab: build
	python3 lab/run.py
clean-lab:
	python3 lab/run.py --cleanup
regression:
	python3 lab/regression.py
