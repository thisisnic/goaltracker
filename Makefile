.PHONY: build test vet run

build:
	go build -o lifeo ./cmd/lifeo

test:
	go test ./...

vet:
	go vet ./...

run: build
	./lifeo
