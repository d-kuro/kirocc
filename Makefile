export GOEXPERIMENT := jsonv2

BIN := dist/kirocc

.PHONY: build install run debug test test-e2e lint vet fmt fix clean

build:
	go build -o $(BIN) ./cmd/kirocc

install:
	go install ./cmd/kirocc

run:
	go run ./cmd/kirocc $(ARGS)

debug:
	go run ./cmd/kirocc -debug $(ARGS)

test:
	go test -race ./...

test-e2e:
	go test -tags e2e -race -timeout 120s ./internal/e2e/

lint:
	golangci-lint run

vet:
	go vet ./...

fmt:
	golangci-lint fmt

fix:
	go fix ./...

clean:
	rm -f $(BIN)
