.PHONY: build test vet race check run1 run2 clean

build:
	go build -o bin/node ./cmd/node
	go build -o bin/p2pctl ./cmd/p2pctl

test:
	go test ./... -count=1 -timeout 60s

vet:
	go vet ./...

race:
	go test -race ./... -count=1 -timeout 120s

# The gate to run before every tag.
check: vet test

run1:
	go run ./cmd/node -id node1 -http 127.0.0.1:8081 -tcp 127.0.0.1:7081 -udp 127.0.0.1:7081

run2:
	go run ./cmd/node -id node2 -http 127.0.0.1:8082 -tcp 127.0.0.1:7082 -udp 127.0.0.1:7082

clean:
	rm -rf bin
