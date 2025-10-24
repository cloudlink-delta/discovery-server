package main

import (
	"os"
	"os/signal"
	"syscall"

	discovery "github.com/cloudlink-delta/discovery-server/server"
)

func main() {

	// Define a globally unique designation that will be used to identify this discovery server.
	const DESIGNATION = "discovery@US-NKY-1"

	// Initialize the discovery server
	instance := discovery.New(DESIGNATION)

	// Graceful shutdown handler
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		instance.Close <- true
		<-instance.Done
		os.Exit(1)
	}()

	// Run the server
	instance.Run()
}
