package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudlink-delta/discovery-server/server"
	"github.com/cloudlink-delta/discovery-server/server/microkey"
)

func main() {
	var privB64, pubB64 string
	privB64, pubB64, _ = microkey.GetKeypair("./priv.key", "./pub.key")
	fmt.Printf("Your private key is: %s\n", privB64)
	fmt.Printf("Your public key is: %s\n", pubB64)

	// Define a globally unique designation that will be used to identify this discovery server.
	const DESIGNATION = "discovery@US-NKY-1"

	s := server.NewServer(DESIGNATION)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		s.Close <- true
		<-s.Done
		os.Exit(1)
	}()
	s.Run()
}
