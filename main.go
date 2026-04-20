package main

import (
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/cloudlink-delta/discovery-server/server"
	"github.com/cloudlink-delta/duplex"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

func main() {

	// CLI flags
	pflag.Int("log-level", (int)(zerolog.InfoLevel), "Logging level to use. Acceptable values range from -1 to 7. (default: 1 \"Info\")")
	pflag.String("config", "", "Path to JSON configuration file")
	pflag.String("designation", "", "Globally unique designation (required)")

	// Duplex lib flags
	pflag.Bool("enable-pinger", false, "Enable ping/pong keepalive")
	pflag.Int64("ping-interval", 5000, "Ping/pong interval (in milliseconds)")
	pflag.Bool("session-secure", true, "Enable secure session server connections (required if session-hostname is set)")
	pflag.Int("session-port", 443, "Port where the session server is listening (required if session-hostname is set)")
	pflag.String("session-hostname", "", "Hostname where the session server is listening")
	pflag.String("ice-servers", "", "JSON-encoded array of ICE servers")
	pflag.String("address", "127.0.0.1:3001", "Discovery server listener address")

	// Parse command-line flags
	pflag.Usage = func() {
		log.Println("Usage: discovery-server [options]")
		log.Println("Options:")
		pflag.PrintDefaults()
	}
	pflag.Parse()

	// Bind flags to viper
	viper.BindPFlag("log_level", pflag.Lookup("log-level"))
	viper.BindPFlag("config", pflag.Lookup("config"))
	viper.BindPFlag("designation", pflag.Lookup("designation"))
	viper.BindPFlag("enable_pinger", pflag.Lookup("enable-pinger"))
	viper.BindPFlag("ping_interval", pflag.Lookup("ping-interval"))
	viper.BindPFlag("session_secure", pflag.Lookup("session-secure"))
	viper.BindPFlag("session_port", pflag.Lookup("session-port"))
	viper.BindPFlag("session_hostname", pflag.Lookup("session-hostname"))
	viper.BindPFlag("ice_servers_flag", pflag.Lookup("ice-servers"))
	viper.BindPFlag("address", pflag.Lookup("address"))

	// Load values from environment variables
	viper.AutomaticEnv()

	// Load config from file if provided
	if cfgFile := viper.GetString("config"); cfgFile != "" {
		viper.SetConfigFile(cfgFile)
		if err := viper.ReadInConfig(); err != nil {
			log.Fatalf("Failed to read config file: %v", err)
		}
	}

	logging_level := zerolog.Level(viper.GetInt("log_level"))
	serverCfg := server.Config{
		Designation: viper.GetString("designation"),
		Address:     viper.GetString("address"),
		Log_Level:   logging_level,
	}

	duplexCfg := duplex.Config{
		LogLevel:     logging_level,
		PingInterval: viper.GetInt64("ping_interval"),
		EnablePinger: viper.GetBool("enable_pinger"),
		Secure:       viper.GetBool("session_secure"),
		Port:         viper.GetInt("session_port"),
	}

	sessionHostnameProvided := pflag.CommandLine.Changed("session-hostname") || viper.IsSet("session_hostname")
	sessionSecureProvided := pflag.CommandLine.Changed("session-secure") || viper.IsSet("session_secure")
	sessionPortProvided := pflag.CommandLine.Changed("session-port") || viper.IsSet("session_port")

	if sessionHostnameProvided {
		duplexCfg.Hostname = viper.GetString("session_hostname")
	}

	var iceServers []webrtc.ICEServer
	if viper.IsSet("ice_servers") {
		data, _ := json.Marshal(viper.Get("ice_servers"))
		if err := json.Unmarshal(data, &iceServers); err != nil {
			log.Fatalf("Failed to parse ice_servers from config: %v", err)
		}
		duplexCfg.ICEServers = iceServers
	}
	if iceFlag := viper.GetString("ice_servers_flag"); iceFlag != "" {
		if err := json.Unmarshal([]byte(iceFlag), &iceServers); err != nil {
			log.Fatalf("Failed to parse ice-servers flag: %v", err)
		}
		duplexCfg.ICEServers = iceServers
	}

	// Verify loaded configuration
	if serverCfg.Designation == "" {
		log.Fatal("A designation is required. Please provide it via -designation or in a config.json file. See --help for more information.")
	}
	if sessionHostnameProvided {
		if !sessionSecureProvided || !sessionPortProvided {
			log.Fatal("When session-hostname is provided, both session-secure and session-port must also be provided explicitly. See --help for more information.")
		}
	}

	// Initialize the discovery server
	instance := server.New(&serverCfg, &duplexCfg)

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
