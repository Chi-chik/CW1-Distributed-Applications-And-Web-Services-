package node

import (
	"context"
	"encoding/json"
	"fmt"

	"p2pfs/internal/ipc"
	"p2pfs/internal/state"
)

// PongBody is what a node says about itself in reply to a ping.
type PongBody struct {
	NodeID string           `json:"nodeId"`
	Status state.NodeStatus `json:"status"`
	Peers  int              `json:"peers"`
	Files  int              `json:"files"`
}

// handleMessage is the single dispatcher behind both TCP and UDP.
// Transports deliver validated envelopes; this decides what they mean.
// It reads and updates state only through the Registry.
func (n *Node) handleMessage(ctx context.Context, req ipc.Envelope) (ipc.Envelope, error) {
	// Hearing from a known peer is evidence it's alive.
	if _, err := n.reg.Peer(req.From); err == nil {
		_ = n.reg.TouchPeer(req.From, 0)
	}

	switch req.Type {
	case ipc.TypePing:
		snap := n.reg.Snapshot()
		return ipc.Reply(req, n.cfg.NodeID, ipc.TypePong, PongBody{
			NodeID: snap.Self.ID, Status: snap.Self.Status, Peers: len(snap.Peers), Files: len(snap.Files),
		})
	case ipc.TypeHeartbeat:
		return ipc.Reply(req, n.cfg.NodeID, ipc.TypeAck, map[string]string{"nodeId": n.cfg.NodeID})
	case ipc.TypeEcho:
		var body json.RawMessage = req.Body
		if body == nil {
			body = json.RawMessage(`null`)
		}
		return ipc.Envelope{V: ipc.ProtocolVersion, Type: ipc.TypeEchoReply, ID: req.ID, From: n.cfg.NodeID, Body: body}, nil
	default:
		return ipc.Envelope{}, fmt.Errorf("message type %q is not a request", req.Type)
	}
}
