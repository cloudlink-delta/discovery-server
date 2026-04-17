package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	discovery "github.com/cloudlink-delta/discovery-server/server"
	"github.com/cloudlink-delta/duplex"
	"github.com/pion/webrtc/v3"
)

func main() {

	// CLI flags
	configFile := flag.String("config", "", "Path to JSON configuration file")
	designationFlag := flag.String("designation", "", "Globally unique designation (required)")

	// Duplex lib flags
	enablePinger := flag.Bool("enable-pinger", false, "Enable ping/pong keepalive")
	pingInterval := flag.Int64("ping-interval", 5000, "Ping/pong interval (in milliseconds)")
	veryVerbose := flag.Bool("very-verbose", false, "Enable very verbose logging (this will absolutely thrash your terminal)")
	enableSecure := flag.Bool("session-secure", true, "Enable secure session server connections (required if session-hostname is set)")
	sessionServerPort := flag.Int("session-port", 443, "Port where the session server is listening (required if session-hostname is set)")
	sessionServerHostname := flag.String("session-hostname", "", "Hostname where the session server is listening")
	iceServersFlag := flag.String("ice-servers", "", "JSON-encoded array of ICE servers")
	address := flag.String("address", "127.0.0.1:3001", "Discovery server listener address")

	// Parse command-line flags
	flag.Usage = func() {
		log.Println("Usage: discovery-server [options]")
		log.Println("Options:")
		flag.PrintDefaults()
	}
	flag.Parse()

	// Defaults
	designation := ""
	duplexCfg := duplex.Config{
		PingInterval: 5000,
		Secure:       true,
		Port:         443,
	}
	listenerAddress := "127.0.0.1:3001"

	// Parser checks
	sessionHostnameProvided := false
	sessionSecureProvided := false
	sessionPortProvided := false

	// Load config from file if provided
	if *configFile != "" {
		data, err := os.ReadFile(*configFile)
		if err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}

		var fileCfg struct {
			Designation     *string            `json:"designation"`
			EnablePinger    *bool              `json:"enable_pinger"`
			PingInterval    *int64             `json:"ping_interval"`
			VeryVerbose     *bool              `json:"very_verbose"`
			ICEServers      []webrtc.ICEServer `json:"ice_servers"`
			SessionHostname *string            `json:"session_hostname"`
			SessionSecure   *bool              `json:"session_secure"`
			SessionPort     *int               `json:"session_port"`
			Address         *string            `json:"address"`
		}

		if err := json.Unmarshal(data, &fileCfg); err != nil {
			log.Fatalf("Failed to parse config file: %v", err)
		}

		if fileCfg.Designation != nil {
			designation = *fileCfg.Designation
		}
		if fileCfg.ICEServers != nil {
			duplexCfg.ICEServers = fileCfg.ICEServers
		}
		if fileCfg.SessionHostname != nil {
			duplexCfg.Hostname = *fileCfg.SessionHostname
			sessionHostnameProvided = true
		}
		if fileCfg.SessionSecure != nil {
			duplexCfg.Secure = *fileCfg.SessionSecure
			sessionSecureProvided = true
		}
		if fileCfg.SessionPort != nil {
			duplexCfg.Port = *fileCfg.SessionPort
			sessionPortProvided = true
		}
		if fileCfg.Address != nil {
			listenerAddress = *fileCfg.Address
		}
		if fileCfg.EnablePinger != nil {
			duplexCfg.EnablePinger = *fileCfg.EnablePinger
		}
		if fileCfg.PingInterval != nil {
			duplexCfg.PingInterval = *fileCfg.PingInterval
		}
		if fileCfg.VeryVerbose != nil {
			duplexCfg.VeryVerbose = *fileCfg.VeryVerbose
		}
	}

	// Override with explicitly set command-line flags
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "designation":
			designation = *designationFlag
		case "enable-pinger":
			duplexCfg.EnablePinger = *enablePinger
		case "ping-interval":
			duplexCfg.PingInterval = *pingInterval
		case "very-verbose":
			duplexCfg.VeryVerbose = *veryVerbose
		case "session-secure":
			duplexCfg.Secure = *enableSecure
			sessionSecureProvided = true
		case "session-port":
			duplexCfg.Port = *sessionServerPort
			sessionPortProvided = true
		case "session-hostname":
			duplexCfg.Hostname = *sessionServerHostname
			sessionHostnameProvided = true
		case "address":
			listenerAddress = *address
		case "ice-servers":
			var iceServers []webrtc.ICEServer
			if err := json.Unmarshal([]byte(*iceServersFlag), &iceServers); err != nil {
				log.Fatalf("Failed to parse ice-servers flag: %v", err)
			}
			duplexCfg.ICEServers = iceServers
		}
	})

	// Verify loaded configuration
	if designation == "" {
		log.Fatal("A designation is required. Please provide it via -designation or in a config.json file. See --help for more information.")
	}
	if sessionHostnameProvided {
		if !sessionSecureProvided || !sessionPortProvided {
			log.Fatal("When session-hostname is provided, both session-secure and session-port must also be provided explicitly. See --help for more information.")
		}
	}

	// Initialize the discovery server
	instance := discovery.New(designation, listenerAddress, &duplexCfg)

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
