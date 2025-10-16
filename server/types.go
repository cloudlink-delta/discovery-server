package server

import (
	"github.com/goccy/go-json"
	peer "github.com/muka/peerjs-go"
)

type Client struct {
	*peer.DataConnection
}

type Instance struct {
	Name         string
	Handler      *peer.Peer
	Close        chan bool
	Done         chan bool
	RetryCounter int
	MaxRetries   int
}

type Packet struct {
	Opcode string `json:"opcode"`
	Origin string `json:"origin,omitempty"`
	Target string `json:"target,omitempty"`
	TTL    int    `json:"ttl,omitempty"`
	Id     string `json:"id,omitempty"`
	Method string `json:"method,omitempty"`
}

type RxPacket struct {
	Packet
	Payload json.RawMessage `json:"payload"`
}

func (p *RxPacket) String() string {
	resp, _ := json.Marshal(p)
	return string(resp)
}

type TxPacket struct {
	Packet
	Payload any `json:"payload"`
}

func (p *TxPacket) String() string {
	resp, _ := json.Marshal(p)
	return string(resp)
}

type NegotiationArgs struct {
	Version     VersionArgs `json:"version"`
	SpecVersion int         `json:"spec_version"`
	Plugins     []string    `json:"plugins"`
	IsBridge    bool        `json:"is_bridge"`
	IsRelay     bool        `json:"is_relay"`
	IsDiscovery bool        `json:"is_discovery"`
}

type VersionArgs struct {
	Type  string `json:"type"`
	Major int    `json:"major"`
	Minor int    `json:"minor"`
	Patch int    `json:"patch"`
}
