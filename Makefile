GO ?= go
BIN := bin/recall

.PHONY: all build run test vet fmt lint tidy clean

all: vet test build

build:
	$(GO) build -o $(BIN) ./cmd/recall

run:
	$(GO) run ./cmd/recall

test:
	$(GO) test ./... -race -cover

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin
