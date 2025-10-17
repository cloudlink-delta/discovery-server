package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudlink-delta/duplex"
)

func main() {

	// Define a globally unique designation that will be used to identify this discovery server.
	const DESIGNATION = "discovery@US-NKY-1"

	// Initialize the discovery server
	instance := duplex.New(DESIGNATION)
	instance.IsDiscovery = true

	// Graceful shutdown handler
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		instance.Close <- true
		<-instance.Done
		os.Exit(1)
	}()

	// Bind opcodes for the discovery server
	instance.Bind("QUERY_PEER", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	instance.Bind("LIST_LOBBIES", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	instance.Bind("REGISTER_LOBBY", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	instance.Bind("UNREGISTER_LOBBY", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	instance.Bind("JOIN_LOBBY", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	instance.Bind("LEAVE_LOBBY", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	// Run the server
	instance.Run()
}
