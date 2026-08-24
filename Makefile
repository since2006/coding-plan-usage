.PHONY: build test vet check

build:
	go build -o coding-plan-usage ./cmd/coding-plan-usage

test:
	go test -race ./...

vet:
	go vet ./...

check: test vet
