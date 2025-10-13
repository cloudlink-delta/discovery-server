package server

import (
	"encoding/json"
	"fmt"

	peer "github.com/muka/peerjs-go"
	"github.com/pion/webrtc/v3"
)

type Client struct {
	*peer.DataConnection
}

type Server struct {
	Name    string
	Handler *peer.Peer
	Close   chan bool
	Done    chan bool
}

func NewServer(designation string) *Server {
	config := peer.NewOptions()
	config.PingInterval = 500
	config.Debug = 3
	config.Configuration.ICEServers = []webrtc.ICEServer{
		{
			URLs: []string{"stun:vpn.mikedev101.cc:3478", "stun:vpn.mikedev101.cc:5349"},
		},
		{
			URLs:       []string{"turn:vpn.mikedev101.cc:5349", "turn:vpn.mikedev101.cc:3478"},
			Username:   "free",
			Credential: "free",
		},
	}

	serverPeer, err := peer.NewPeer(designation, config)
	if err != nil {
		fmt.Println(err)
		return nil
	}

	serverInstance := &Server{
		Name:    designation,
		Handler: serverPeer,
		Close:   make(chan bool),
		Done:    make(chan bool),
	}

	return serverInstance
}

func (s *Server) Run() {
	p := s.Handler
	defer p.Destroy()

	p.On("connection", func(data any) {
		switch c := data.(type) {
		case *peer.DataConnection:
			s.PeerHandler(&Client{c})
		default:
			panic("unhandled data type")
		}
	})

	p.On("error", func(data any) {
		fmt.Printf("Server error: %v\n", data)
	})

	p.On("open", func(data any) {
		fmt.Printf("Server opened as %s\n", s.Name)
	})

	p.On("close", func(data any) {
		fmt.Println("Server closed")
		s.Done <- true
	})

	<-s.Close
	fmt.Println("\nServer got close signal")
}

func (s *Server) PeerHandler(conn *Client) {
	conn.On("open", func(data any) {
		fmt.Printf("Peer %s connected\n", conn.GetPeerID())
	})

	conn.On("close", func(data any) {
		fmt.Printf("Peer %s disconnected\n", conn.GetPeerID())
	})

	conn.On("error", func(data any) {
		fmt.Printf("Peer %s error: %v\n", conn.GetPeerID(), data)
	})

	conn.On("data", func(data any) {
		byteStream := data.([]byte)

		// Trim (UTF-16) header???
		byteStream = byteStream[3:]

		fmt.Printf("Received data from peer %s: %s\n", conn.GetPeerID(), byteStream)

		packet := struct {
			Opcode  string `json:"opcode"`
			Payload any    `json:"payload"`
		}{}
		err := json.Unmarshal(fmt.Appendf(nil, "%s", byteStream), &packet)
		if err != nil {
			fmt.Println(err)
			return
		}

		// TODO...
	})
}
