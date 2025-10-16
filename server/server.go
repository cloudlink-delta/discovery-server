package server

import (
	"log"
	"math"
	"time"

	peer "github.com/muka/peerjs-go"
	"github.com/pion/webrtc/v3"
)

func NewServer(designation string) *Server {
	config := peer.NewOptions()
	config.PingInterval = 500
	config.Debug = 2
	config.Host = "peerjs.mikedev101.cc"
	config.Port = 443
	config.Secure = true
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

	log.Println("Starting server...")
	serverPeer, err := peer.NewPeer(designation, config)
	if err != nil {
		log.Println(err)
		return nil
	}

	serverInstance := &Server{
		Name:         designation,
		Handler:      serverPeer,
		Close:        make(chan bool),
		Done:         make(chan bool),
		RetryCounter: 0,
		MaxRetries:   5,
	}

	return serverInstance
}

func (s *Server) Run() {
	provider := s.Handler
	defer provider.Destroy()

	provider.On("connection", func(data any) {
		switch c := data.(type) {
		case *peer.DataConnection:
			s.PeerHandler(&Client{c})
		default:
			panic("unhandled data type")
		}
	})

	provider.On("error", func(data any) {
		log.Printf("Server error: %v", data)

		// TODO: Improve error handling
		if data.(string) == "Lost connection to server" {
			s.RetryCounter++

			if s.RetryCounter > s.MaxRetries {
				log.Println("Max retries reached")
				s.Done <- true
				return
			}

			if s.RetryCounter > 0 {
				time.Sleep(time.Duration(math.Pow(2, float64(s.RetryCounter))) * time.Second)
			}

			log.Printf("Attempting to reconnect... (%d of %d attempts)", s.RetryCounter, s.MaxRetries)
			provider.Reconnect()
		}
	})

	provider.On("open", func(data any) {
		s.RetryCounter = 0
		log.Printf("Server opened as %s", s.Name)
	})

	provider.On("close", func(data any) {
		log.Println("Server closed")
		s.Done <- true
	})

	<-s.Close
	log.Println("\nServer got close signal")
}

func (s *Server) PeerHandler(conn *Client) {
	conn.On("open", func(data any) {
		log.Printf("%s connected", conn.GiveName())
		log.Println(conn.Metadata)
	})

	conn.On("close", func(data any) {
		log.Printf("%s disconnected", conn.GiveName())
	})

	conn.On("error", func(data any) {
		log.Printf("%s error: %v", conn.GiveName(), data)
	})

	conn.On("data", func(data any) {
		packet := conn.Read(data)
		if packet == nil {
			return
		}
		log.Printf("%s 🢂 %v", conn.GiveName(), packet)
		go conn.HandlePacket(packet)
	})
}
