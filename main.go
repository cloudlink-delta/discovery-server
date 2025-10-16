package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudlink-delta/duplex"
)

func main() {
	// Define a globally unique designation that will be used to identify this discovery server.
	const DESIGNATION = "US-NKY-1"

	peer := duplex.New(DESIGNATION)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		peer.Close <- true
		<-peer.Done
		os.Exit(1)
	}()
	peer.Run()
}
