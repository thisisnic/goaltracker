.PHONY: build test vet run

build:
	go build -o goaltracker ./cmd/goaltracker

test:
	go test ./...

vet:
	go vet ./...

run: build
	./goaltracker
