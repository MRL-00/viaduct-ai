BINARY := viaduct
MAIN := ./cmd/viaduct

.PHONY: build test lint run docker

build:
	go build -o bin/$(BINARY) $(MAIN)

test:
	go test ./...

lint:
	go vet ./...

run:
	go run $(MAIN)

docker:
	docker build -t viaduct:latest .
