export GOEXPERIMENT := jsonv2

BIN := dist/kirocc

.PHONY: build install run debug test lint fmt fix clean

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

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

fix:
	go fix ./...

clean:
	rm -f $(BIN)
