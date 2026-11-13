package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPeerLifecycleOverREST(t *testing.T) {
	h, reg := newTestAPI(t)

	rec := do(h, "POST", "/v1/peers", "application/json", `{"id":"node2","addrs":{"grpc":"127.0.0.1:9002"}}`)
	if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/v1/peers/node2" {
		t.Fatalf("create: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}

	rec = do(h, "POST", "/v1/peers", "application/json", `{"id":"node2","addrs":{"grpc":"127.0.0.1:9999"}}`)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "conflict" {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", "/v1/peers", "", "")
	var list peerList
	json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Count != 1 || list.Peers[0].Addrs.GRPC != "127.0.0.1:9002" {
		t.Fatalf("list: %s", rec.Body)
	}

	if rec = do(h, "DELETE", "/v1/peers/node2", "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec = do(h, "GET", "/v1/peers/node2", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
	if len(reg.Peers()) != 0 {
		t.Fatal("peer still in registry")
	}
}

func TestPeerRejectionsOverREST(t *testing.T) {
	cases := []struct{ name, body, code string }{
		{"server field", `{"id":"n2","addrs":{"grpc":"h:1"},"source":"gossip"}`, "unknown_field"},
		{"bad address", `{"id":"n2","addrs":{"grpc":"nohost"}}`, "invalid_value"},
		{"self", `{"id":"node1","addrs":{"grpc":"h:1"}}`, "invalid_value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, reg := newTestAPI(t)
			rec := do(h, "POST", "/v1/peers", "application/json", tc.body)
			if got := errorCode(t, rec); got != tc.code {
				t.Fatalf("code %q, want %q (%d)", got, tc.code, rec.Code)
			}
			if len(reg.Peers()) != 0 {
				t.Fatal("state changed")
			}
		})
	}
}

func TestFileMetadataOverREST(t *testing.T) {
	h, _ := newTestAPI(t)
	body := `{"name":"report.txt","size":11,"sha256":"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"}`
	if rec := do(h, "POST", "/v1/files", "application/json", body); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := do(h, "POST", "/v1/files", "application/json", `{"name":"../x","size":1,"sha256":"b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("traversal name: %d", rec.Code)
	}
	if rec := do(h, "GET", "/v1/files/report.txt", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
}
