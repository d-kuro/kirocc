export GOEXPERIMENT := jsonv2

BIN := dist/kirocc

.PHONY: build install run debug test test-e2e lint fmt fix clean

build:
	go build -o $(BIN) ./cmd/kirocc

install:
	go install ./cmd/kirocc

run:
	go run ./cmd/kirocc

debug:
	go run ./cmd/kirocc -debug

test:
	go test -race ./...

test-e2e:
	go test -tags e2e -race -timeout 120s ./internal/e2e/

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

fix:
	go fix ./...

clean:
	rm -f $(BIN)
