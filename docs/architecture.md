# Architecture

## CW1: two-node foundation

```mermaid
flowchart LR
  subgraph node1["node1 process"]
    direction TB
    R1["REST :8081<br/>internal/httpapi"]
    T1["TCP/UDP :7081<br/>internal/ipc"]
    G1["gRPC :9081<br/>internal/rpc"]
    S1[("Registry<br/>internal/state<br/>self · peers · files")]
    P1["rpc.Pool<br/>(client)"]
    R1 --> S1
    T1 --> S1
    G1 --> S1
    R1 -- "probe / remote files" --> P1
  end
  subgraph node2["node2 process"]
    direction TB
    R2["REST :8082"]
    T2["TCP/UDP :7082"]
    G2["gRPC :9082"]
    S2[("Registry")]
    R2 --> S2
    T2 --> S2
    G2 --> S2
  end
  P1 -- "ListFiles / GetNodeInfo (HTTP/2)" --> G2
  R1 -. "ping (framed TCP, datagram UDP)" .-> T2
  user(["curl / p2pctl"]) --> R1
```

**One rule:** only `state.Registry` holds mutable shared data. Its methods lock internally and copy values in and out, so no other package can reach the maps.

**Lifecycle:** `node.New` binds all four listeners and releases the ones it already bound if a later one fails; `Run` serves them as components; shutdown stops them in reverse order within `ShutdownTimeout`, then closes the client pool.
