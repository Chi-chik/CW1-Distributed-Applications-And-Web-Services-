.PHONY: build test vet race check run1 run2 clean

build:
	go build -o bin/node ./cmd/node

test:
	go test ./... -count=1 -timeout 60s

vet:
	go vet ./...

race:
	go test -race ./... -count=1 -timeout 120s

# The gate to run before every tag.
check: vet test

run1:
	go run ./cmd/node -id node1 -http 127.0.0.1:8081

run2:
	go run ./cmd/node -id node2 -http 127.0.0.1:8082

clean:
	rm -rf bin
