#!/usr/bin/env bash
# Two-node CW1 demonstration: REST, state, TCP, UDP and gRPC, with failure cases.
# Usage: scripts/demo-cw1.sh        (from the repository root; needs go, curl)
set -uo pipefail
cd "$(dirname "$0")/../source"

BIN=$(mktemp -d)
LOGS=$(mktemp -d)
go build -o "$BIN/node" ./cmd/node && go build -o "$BIN/p2pctl" ./cmd/p2pctl || exit 1

N1="-id node1 -http 127.0.0.1:8081 -tcp 127.0.0.1:7081 -udp 127.0.0.1:7081 -grpc 127.0.0.1:9081"
N2="-id node2 -http 127.0.0.1:8082 -tcp 127.0.0.1:7082 -udp 127.0.0.1:7082 -grpc 127.0.0.1:9082"
P2="node2,tcp=127.0.0.1:7082,udp=127.0.0.1:7082,grpc=127.0.0.1:9082"
P1="node1,tcp=127.0.0.1:7081,udp=127.0.0.1:7081,grpc=127.0.0.1:9081"

"$BIN/node" $N1 -peer "$P2" 2>"$LOGS/node1.log" & PID1=$!
"$BIN/node" $N2 -peer "$P1" 2>"$LOGS/node2.log" & PID2=$!
cleanup() { kill -TERM $PID1 $PID2 2>/dev/null; wait 2>/dev/null; rm -rf "$BIN"; }
trap cleanup EXIT
for p in 8081 8082; do
  for _ in $(seq 1 50); do curl -sf "localhost:$p/healthz" >/dev/null && break; sleep 0.1; done
done

step() { printf '\n\033[1m== %s\033[0m\n$ %s\n' "$1" "$2"; eval "$2"; echo; }
J='-H Content-Type:application/json'

step "1. Health of both nodes"              "curl -s localhost:8081/healthz; curl -s localhost:8082/healthz"
step "2. REST state change on node2"        "curl -s -X POST localhost:8082/v1/node/status $J -d '{\"status\":\"draining\",\"reason\":\"demo\"}'"
step "3. Register file metadata on node2"   "curl -s -X POST localhost:8082/v1/files $J -d '{\"name\":\"hello.txt\",\"size\":11,\"sha256\":\"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9\"}'"
step "4. node1 probes node2 (TCP+UDP+gRPC)" "curl -s -X POST localhost:8081/v1/peers/node2/probe"
step "5. node1 lists node2's files (gRPC)"  "curl -s localhost:8081/v1/peers/node2/files"
step "6. Direct TCP ping with p2pctl"       "'$BIN/p2pctl' tcp-ping -addr 127.0.0.1:7082 -from node1"
step "7. Direct gRPC call with p2pctl"      "'$BIN/p2pctl' info -addr 127.0.0.1:9082"

printf '\n\033[1m-------- negative cases --------\033[0m\n'
step "N1. Malformed JSON -> 400"            "curl -s -o /dev/stderr -w 'HTTP %{http_code}\n' -X POST localhost:8081/v1/peers $J -d '{\"id\":' 2>&1"
step "N2. Unknown field -> 400"             "curl -s -w '\nHTTP %{http_code}\n' -X POST localhost:8081/v1/node/status $J -d '{\"status\":\"active\",\"admin\":true}'"
step "N3. Wrong media type -> 415"          "curl -s -w '\nHTTP %{http_code}\n' -X POST localhost:8081/v1/node/status -H 'Content-Type: text/plain' -d x"
step "N4. Path traversal name -> 422"       "curl -s -w '\nHTTP %{http_code}\n' -X POST localhost:8081/v1/files $J -d '{\"name\":\"../etc/passwd\",\"size\":1,\"sha256\":\"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9\"}'"
step "N5. Duplicate peer -> 409"            "curl -s -w '\nHTTP %{http_code}\n' -X POST localhost:8081/v1/peers $J -d '{\"id\":\"node2\",\"addrs\":{\"grpc\":\"127.0.0.1:9082\"}}'"
step "N6. Invalid gRPC request"             "'$BIN/p2pctl' files -addr 127.0.0.1:9082 -prefix '../x'"
step "N7. UDP to closed port"               "'$BIN/p2pctl' udp-ping -addr 127.0.0.1:7999 -timeout 600ms"
step "N8. Port already in use -> exit 1"    "'$BIN/node' $N1 2>&1 | tail -1; echo \"exit code: \${PIPESTATUS[0]}\""

printf '\n\033[1m-------- peer failure --------\033[0m\n'
kill -TERM $PID2; wait $PID2 2>/dev/null
step "F1. node2 stopped; node1 lists files -> 502 within bound" "time curl -s -w '\nHTTP %{http_code}\n' localhost:8081/v1/peers/node2/files"
step "F2. node1 still healthy"              "curl -s localhost:8081/healthz"

kill -TERM $PID1; wait $PID1 2>/dev/null
echo "== Shutdown logs (last 2 lines each)"
tail -n 2 "$LOGS/node1.log" "$LOGS/node2.log"
rm -rf "$LOGS"
