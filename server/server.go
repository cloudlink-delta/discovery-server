package server

import (
	"fmt"
	"log"
	"maps"
	"regexp"
	"slices"
	"sync"

	"github.com/goccy/go-json"
	"github.com/google/uuid"

	"github.com/cloudlink-delta/duplex"
)

// Define a struct to hold lobby data
type Lobby struct {
	ID               any           `json:"lobby_id"`          // ID of the lobby
	Host             string        `json:"host"`              // Host of the lobby
	CurrentPeers     int64         `json:"current_peers"`     // Number of peers currently in the lobby
	MaxPeers         int64         `json:"max_peers"`         // Maximum number of peers allowed in the lobby
	PasswordRequired bool          `json:"password_required"` // Whether the lobby requires a password
	Hidden           bool          `json:"hidden"`            // Whether the lobby is hidden
	Locked           bool          `json:"locked"`            // Whether the lobby is locked
	Metadata         any           `json:"metadata"`          // Arbitrary, user-defined storage
	Peers            []*PeerObject `json:"peers"`             // List of peers in the lobby (shortcut for querying peers)

	Password    string     `json:"-"` // Password for the lobby
	Instance    *Instance  `json:"-"` // Pointer to the instance
	*sync.Mutex `json:"-"` // Mutex for thread safety
}

type PeerObject struct {
	Online        bool   `json:"online"`
	Username      string `json:"username,omitempty"`
	Designation   string `json:"designation,omitempty"`
	InstanceID    string `json:"instance_id,omitempty"`
	IsLobbyMember bool   `json:"is_lobby_member,omitempty"`
	IsLobbyHost   bool   `json:"is_lobby_host,omitempty"`
	IsInLobby     bool   `json:"is_in_lobby,omitempty"`
	LobbyID       string `json:"lobby_id,omitempty"`
	RTT           int64  `json:"rtt,omitempty"`
	IsLegacy      bool   `json:"is_legacy,omitempty"`
	IsRelayed     bool   `json:"is_relayed,omitempty"`
	RelayPeer     string `json:"relay_peer,omitempty"`
	IsBridge      bool   `json:"is_bridge,omitempty"`
	IsDiscovery   bool   `json:"is_discovery,omitempty"`
}

// Define type aliases
type Lobbies map[string]*Lobby
type Hosts map[*Lobby]*duplex.Peer
type Peers map[*Lobby][]*duplex.Peer

func (l Lobbies) ToSlice() []string {
	var lobbies []string
	for _, lobby := range l {
		if lobby.Hidden {
			continue
		}
		if lobby.Password != "" {
			continue
		}
		if lobby.Locked {
			continue
		}
		lobbies = append(lobbies, AnyToString(lobby.ID))
	}
	if len(lobbies) == 0 {
		return []string{}
	}
	return lobbies
}

func (l *Lobby) Remove(peer *duplex.Peer) {
	if l == nil || l.Instance == nil || l.Instance.Members == nil || peer == nil {
		return
	}
	peers, ok := l.Instance.Members[l]
	if !ok {
		return
	}
	idx := slices.Index(peers, peer)
	if idx == -1 {
		return
	}
	l.Instance.Members[l] = slices.Delete(peers, idx, 1)
}

func (l *Lobby) PrecomputeTasks() {
	l.GetHost()
	l.ComputeCount()
	l.GetPeers()
}

func (l *Lobby) GetHost() {
	if len(l.Instance.Members[l]) > 0 {
		l.Host = l.Instance.Members[l][0].GetPeerID()
	} else {
		l.Host = ""
	}
}

func (l *Lobby) GetPeers() {
	l.Peers = make([]*PeerObject, len(l.Instance.Members[l]))
	for i, peer := range l.Instance.Members[l] {
		_, isHost, _ := l.Instance.GetState(peer, false, false, nil)
		l.Peers[i] = &PeerObject{
			Username:    peer.GiveName(),
			Designation: l.Instance.Designation,
			InstanceID:  peer.GetPeerID(),
			IsLobbyHost: isHost,
			RTT:         peer.RTT,
			IsBridge:    peer.IsBridge,
			IsDiscovery: peer.IsDiscovery,
		}
	}
}

func (l *Lobby) ComputeCount() {
	l.CurrentPeers = int64(len(l.Instance.Members[l]))
}

type Registry map[string]*duplex.Peer

// Define Discovery server
type Instance struct {
	Designation       string
	Lobbies           Lobbies
	Hosts             Hosts
	Members           Peers
	Mutex             *sync.Mutex
	NameRegistry      Registry
	BridgeRegistry    Registry
	DiscoveryRegistry Registry
	*duplex.Instance
}

func New(designation string, config *duplex.Config) *Instance {

	// Initialize duplex instance
	server := &Instance{
		Designation:       designation,
		Instance:          duplex.New("discovery@"+designation, config),
		Lobbies:           make(Lobbies),
		Hosts:             make(Hosts),
		Members:           make(Peers),
		Mutex:             &sync.Mutex{},
		NameRegistry:      make(Registry),
		BridgeRegistry:    make(Registry),
		DiscoveryRegistry: make(Registry),
	}
	server.IsDiscovery = true

	// server.OnOpen gets called immediately when a peer connects.
	server.OnOpen = func(_ *duplex.Peer) {}

	server.OnBridgeConnected = func(peer *duplex.Peer) {

		// Automatically register the bridge
		server.AutoRegister(peer, server.BridgeRegistry)

		// Announce to all other connected peers that the server is available
		server.Broadcast(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "DISCOVER",
				TTL:    1,
			},
			Payload: peer.GetPeerID(),
		}, server.Peers.ToSlice(peer))
	}

	server.OnDiscoveryConnected = func(peer *duplex.Peer) {

		// Automatically register the discovery server
		server.AutoRegister(peer, server.DiscoveryRegistry)

	}

	// server.AfterNegotiation gets called after both our peer and the peer we just connected negotiates successfully.
	server.AfterNegotiation = func(peer *duplex.Peer) {

		// Obtain lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		// Return current lobby list
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "LOBBY_LIST",
				TTL:    1,
			},
			Payload: server.Lobbies.ToSlice(),
		})

		// Announce all connected bridge servers available for connecting
		for bridge := range server.BridgeRegistry {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "DISCOVER",
					TTL:    1,
				},
				Payload: bridge,
			})
		}

		// Spawn a thread that periodically PING/PONGs the newly connected peer to calculate RTT
		go server.SpawnTicker(peer)
	}

	// server.OnClose gets called when a peer disconnects.
	server.OnClose = func(peer *duplex.Peer) {

		// Read name
		name, name_set := peer.KeyStore["name"]
		if name_set {
			if _n, ok := name.(string); ok {
				delete(server.NameRegistry, _n)
				delete(server.BridgeRegistry, _n)
			}
		}

		// Read state
		lobby, host, _ := server.GetState(peer, false, false, &duplex.RxPacket{})

		// Do nothing if they are not in a lobby
		if lobby == nil {
			return
		}

		// Get lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		// Perform host actions
		if host {

			// Unset lobby host
			delete(server.Hosts, lobby)

			// Pick a new host
			if len(server.Members[lobby]) > 0 {
				new_host := server.Members[lobby][0]
				server.Hosts[lobby] = new_host
				lobby.Remove(new_host)

				// tell peers they are now the host, also tell them the old host is leaving
				for _, p := range server.Members[lobby] {
					if p == peer {
						continue
					}
					go p.Write(&duplex.TxPacket{
						Packet: duplex.Packet{
							Opcode: "PEER_LEFT",
							TTL:    1,
						},
						Payload: peer.GetPeerID(),
					})
					go p.Write(&duplex.TxPacket{
						Packet: duplex.Packet{
							Opcode: "NEW_HOST",
							TTL:    1,
						},
						Payload: new_host.GetPeerID(),
					})
				}

				// Transition to host mode
				new_host.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode: "TRANSITION",
						TTL:    1,
					},
					Payload: "host",
				})

				log.Printf("made %s the new host of lobby %v", new_host.GetPeerID(), lobby.ID)

			} else {

				// Destroy the lobby if there are no more members
				delete(server.Lobbies, AnyToString(lobby.ID))
				delete(server.Hosts, lobby)
				delete(server.Members, lobby)

				// Tell all peers the lobby has been destroyed
				for _, p := range server.Peers {
					if p == peer {
						continue
					}
					go p.Write(&duplex.TxPacket{
						Packet: duplex.Packet{
							Opcode: "LOBBY_CLOSED",
							TTL:    1,
						},
						Payload: AnyToString(lobby.ID),
					})
				}

				log.Printf("destroyed lobby %v since it was empty", lobby.ID)
			}

		} else {
			// Perform member actions

			// Remove from members
			lobby.Remove(peer)

			log.Printf("removed %s from lobby %v", peer.GetPeerID(), lobby.ID)
		}

		// TODO: halt and destroy the PING/PONG thread
	}

	// Bind opcode handlers
	server.Bind("LOBBY_INFO", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Unmarshal target
		var target any
		if err := json.Unmarshal(packet.Payload, &target); err != nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			return
		}

		// Get lobby
		lobby, exists := server.Lobbies[AnyToString(target)]
		if !exists {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "LOBBY_NOTFOUND",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: fmt.Sprintf("Lobby %v does not exist", target),
			})
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Perform precomputations
		lobby.PrecomputeTasks()

		// Return status
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "LOBBY_INFO",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: lobby,
		})
	})

	/*
	 * CONFIG_HOST is a request to create a new lobby.
	 * {
	 *   "lobby_id": any,
	 *   "password": string,
	 *   "max_peers": int,
	 *   "locked": bool,
	 *   "hidden": bool,
	 *   "metadata": any
	 * }
	 */
	server.Bind("CONFIG_HOST", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Define arguments for the CONFIG_HOST opcode
		type ConfigHostArgs struct {
			LobbyID  any    `json:"lobby_id"`
			Password string `json:"password"`
			MaxPeers *int64 `json:"max_peers"`
			Locked   bool   `json:"locked"`
			Hidden   bool   `json:"hidden"`
			Metadata any    `json:"metadata"`
		}

		// Read arguments
		var args ConfigHostArgs
		if err := json.Unmarshal(packet.Payload, &args); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		// Obtain lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		// Check if a lobby entry exists
		if _, exists := server.Lobbies[AnyToString(args.LobbyID)]; exists {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "LOBBY_EXISTS",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: args.LobbyID,
			})
			return
		}

		// TODO: validate arguments (metadata needs to be marshalable, max peer count can't be negative or zero, etc.)

		// Validate type of ID
		if err := ValidateAnyType(args.LobbyID); err != nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "lobby_id: " + err.Error(),
			})
			return
		}

		// If args.MaxPeers is nil, use -1
		if args.MaxPeers == nil {
			args.MaxPeers = new(int64)
			*args.MaxPeers = -1
		}

		// Require max peer count to be at least 1. If the value is -1, allow an unlimited number of peers.
		if !(*args.MaxPeers == -1) && *args.MaxPeers <= 0 {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "max_peers cannot be negative",
			})
			return
		}

		// Create lobby
		lobby := &Lobby{
			ID:               args.LobbyID,
			MaxPeers:         *args.MaxPeers,
			Locked:           args.Locked,
			Hidden:           args.Hidden,
			Password:         args.Password,
			PasswordRequired: args.Password != "",
			Metadata:         args.Metadata,
			Mutex:            &sync.Mutex{},
			Instance:         server,
		}

		// Add lobby to server
		server.Lobbies[AnyToString(args.LobbyID)] = lobby
		server.Hosts[lobby] = peer

		// Set key
		peer.KeyStore["lobby"] = args.LobbyID

		// Log
		log.Printf("%s created %v", peer.GiveName(), args.LobbyID)

		// Tell the peer to TRANSITION to "host"
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "TRANSITION",
				TTL:    1,
			},
			Payload: "host",
		})

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "CONFIG_HOST_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: args.LobbyID,
		})

		// If hidden, do not broadcast
		if args.Hidden {
			return
		}

		// If locked, do not broadcast
		if args.Locked {
			return
		}

		// If password is set, do not broadcast
		if args.Password != "" {
			return
		}

		// Tell all peers about the new lobby (except the host)
		server.Broadcast(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "NEW_LOBBY",
				TTL:    1,
			},
			Payload: lobby.ID,
		}, server.Peers.ToSlice(peer))
	})

	/* CONFIG_PEER is a request to join a lobby.
	 * {
	 *   "lobby_id": any,
	 *   "password": string
	 * }
	 */
	server.Bind("CONFIG_PEER", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Define arguments for the CONFIG_PEER opcode
		type ConfigPeerArgs struct {
			LobbyID  any    `json:"lobby_id"`
			Password string `json:"password"`
		}

		// Read arguments
		var args ConfigPeerArgs
		if err := json.Unmarshal(packet.Payload, &args); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		// Check if a lobby entry exists
		if _, exists := server.Lobbies[AnyToString(args.LobbyID)]; !exists {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "LOBBY_NOTFOUND",
					Listener: packet.Listener,
					TTL:      1,
				},
			})
			return
		}

		// Get lobby
		lobby := server.Lobbies[AnyToString(args.LobbyID)]
		if lobby == nil {
			panic("lobby is nil, despite previous validation check passing")
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Check if locked
		if lobby.Locked {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "LOBBY_LOCKED",
					Listener: packet.Listener,
					TTL:      1,
				},
			})
			return
		}

		// Validate current count and state
		if lobby.MaxPeers != -1 && int64(len(server.Members[lobby])) >= lobby.MaxPeers {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "LOBBY_FULL",
					Listener: packet.Listener,
					TTL:      1,
				},
			})
			return
		}

		// Validate password (if present)
		if lobby.Password != "" {
			if args.Password == "" {

				// Peer did not provide a password
				peer.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode:   "PASSWORD_REQUIRED",
						Listener: packet.Listener,
						TTL:      1,
					},
				})
				return

			} else if args.Password != lobby.Password {

				// Password is incorrect
				peer.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode:   "PASSWORD_FAIL",
						Listener: packet.Listener,
						TTL:      1,
					},
				})
				return

			} else {

				// Password is correct
				peer.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode: "PASSWORD_ACK",
						TTL:    1,
					},
				})
			}
		}

		// Add peer to lobby
		server.Members[lobby] = append(server.Members[lobby], peer)

		// Set key
		peer.KeyStore["lobby"] = args.LobbyID

		// Log
		log.Printf("%s joined %v", peer.GiveName(), lobby)

		// Tell the peer to TRANSITION to "peer"
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "TRANSITION",
				TTL:    1,
			},
			Payload: "peer",
		})

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "CONFIG_PEER_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: args.LobbyID,
		})

		// Notify members
		for _, p := range server.Members[lobby] {
			if p != peer {
				go p.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode: "PEER_JOIN",
						TTL:    1,
					},
					Payload: peer.GetPeerID(),
				})
			}
		}

		// Notify host
		host := server.Hosts[lobby]
		go host.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "PEER_JOIN",
				TTL:    1,
			},
			Payload: peer.GetPeerID(),
		})
	})

	// LOBBY_LIST is a request for a list of lobbies.
	server.Bind("LOBBY_LIST", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Obtain lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		// Return current lobby list
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "LOBBY_LIST",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: server.Lobbies.ToSlice(),
		})
	})

	// LOCK is an administrative command that can lock access to a lobby.
	server.Bind("LOCK", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Lock lobby
		lobby.Locked = true
		log.Printf("%s locked %s", peer.GiveName(), lobby.ID)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "LOCK_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
	})

	// UNLOCK is an administrative command that can unlock access to a lobby.
	server.Bind("UNLOCK", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Unlock lobby
		lobby.Locked = false
		log.Printf("%s unlocked %s", peer.GiveName(), lobby.ID)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "UNLOCK_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
	})

	// SIZE is an adminstrative command that can change the max player count of a lobby.
	server.Bind("SIZE", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Validate input
		if packet.Payload == nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "payload: must be an integer",
			})
			return
		}

		// Read new value
		var new_count int64
		if err := json.Unmarshal(packet.Payload, &new_count); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		if new_count != -1 && new_count < 0 {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "payload: integer must be greater than 0 or set to -1",
			})
			return
		}

		// Set new value
		lobby.MaxPeers = new_count
		log.Printf("%s set lobby %s max peers to %d", peer.GiveName(), lobby.ID, lobby.MaxPeers)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "SIZE_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
	})

	// PASSWORD is an adminstrative command that can change the password of a lobby.
	server.Bind("PASSWORD", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Validate input
		if packet.Payload == nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "payload: must be a string",
			})
			return
		}

		// Read new value
		var new_password string
		if err := json.Unmarshal(packet.Payload, &new_password); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		// Set new value
		lobby.Password = new_password
		log.Printf("%s updated lobby %s password", peer.GiveName(), lobby.ID)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "PASSWORD_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
	})

	// KICK is an administrative command that can remove a peer from a lobby.
	server.Bind("KICK", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Validate input
		if packet.Payload == nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "payload: must be a string",
			})
			return
		}

		// Read new value
		var query string
		if err := json.Unmarshal(packet.Payload, &query); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "VIOLATION",
					TTL:    1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		// Find peer based on name
		target, ok := server.NameRegistry[query]
		if !ok {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "KICK_ACK",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: false,
			})
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Leave lobby
		lobby.Remove(target)

		// Notify members
		for _, p := range server.Members[lobby] {
			if p == peer || p == target {
				continue
			}
			go p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "PEER_LEFT",
					TTL:    1,
				},
				Payload: target.GetPeerID(),
			})
		}

		// Notify host
		host := server.Hosts[lobby]
		go host.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "PEER_LEFT",
				TTL:    1,
			},
			Payload: target.GetPeerID(),
		})

		// Tell target they were kicked
		target.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "KICKED",
				TTL:    1,
			},
		})

		log.Printf("%s was kicked from lobby %s by %s", target.GiveName(), lobby.ID, peer.GiveName())

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "KICK_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: true,
		})

	})

	// HIDE is an administrative command that can prevent a lobby from being shown in the lobby list or broadcasts.
	server.Bind("HIDE", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Hide lobby
		lobby.Hidden = true
		log.Printf("%s hid %s", peer.GiveName(), lobby.ID)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "HIDE_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
	})

	// SHOW is an administrative command that can allow a lobby from being shown in the lobby list list or broadcasts.
	server.Bind("SHOW", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Show lobby
		lobby.Hidden = false

		log.Printf("%s showed %s", peer.GiveName(), lobby.ID)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "SHOW_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
	})

	// TRANSFER is an administrative command that can transfer a lobby to another peer.
	server.Bind("TRANSFER", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			return
		}

		// Validate input
		if packet.Payload == nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "payload: must be a string",
			})
			return
		}

		// Read new value
		var query string
		if err := json.Unmarshal(packet.Payload, &query); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		// Find peer based on name
		target, ok := server.NameRegistry[query]
		if !ok {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "TRANSFER_ACK",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: false,
			})
			return
		}

		// Obtain lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		lobby.Lock()
		defer lobby.Unlock()

		// Unset current host
		delete(server.Hosts, lobby)

		// Set new host
		server.Hosts[lobby] = target
		lobby.Remove(target)

		// Transition target to host mode
		target.Write(
			&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "TRANSITION",
					TTL:    1,
				},
				Payload: "host",
			},
		)

		// Transition current host to peer mode
		target.Write(
			&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "TRANSITION",
					TTL:    1,
				},
				Payload: "peer",
			},
		)

		// Notify peers of new host
		for _, p := range server.Members[lobby] {
			if p == peer {
				continue
			}
			go p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "NEW_HOST",
					TTL:    1,
				},
				Payload: target.GetPeerID(),
			})
		}

		log.Printf("%s transferred %s to %s", peer.GiveName(), lobby.ID, target.GetPeerID())

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "TRANSFER_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: true,
		})

	})

	var queryRegex = regexp.MustCompile(`^([^.@]+)(?:\.([^@]+))?(?:@(.+))?$`)

	// QUERY returns details about a connected peer, including the lobby they are in, their RTT to the server, and roles.
	server.Bind("QUERY", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Read the payload as a query argument
		var query string
		if err := json.Unmarshal(packet.Payload, &query); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		matches := queryRegex.FindStringSubmatch(query)
		if matches == nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "Invalid query format",
			})
			peer.Close()
			return
		}

		username := matches[1]
		bridge := matches[2]
		designation := matches[3]

		// 1. If it's for a specific designation that isn't ours, ask discovery servers
		if designation != "" && designation != server.Designation {
			server.Mutex.Lock()
			streams := make(Registry)
			maps.Copy(streams, server.DiscoveryRegistry)
			server.Mutex.Unlock()

			server.Multiquery(query, peer, packet, streams)
			return
		}

		// 2. If it targets a specific bridge
		if bridge != "" {
			server.Mutex.Lock()
			var bridgePeer *duplex.Peer
			var exists bool

			bridgePeer, exists = server.BridgeRegistry[bridge]
			if !exists && designation == "" {
				bridgePeer, exists = server.BridgeRegistry[bridge+"@"+server.Designation]
			}
			if !exists && designation != "" {
				bridgePeer, exists = server.BridgeRegistry[bridge+"@"+designation]
			}
			server.Mutex.Unlock()

			if exists {
				// Ask the specific upstream bridge
				server.Multiquery(query, peer, packet, Registry{bridge: bridgePeer})
			} else {
				// Bridge not found
				peer.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode:   "QUERY_ACK",
						Listener: packet.Listener,
						TTL:      1,
					},
					Payload: PeerObject{
						Username: query,
						Online:   false,
					},
				})
			}
			return
		}

		// 3. Otherwise, use the local resolver
		server.ResolvePeer(username, peer, packet)

	})

	// LEAVE is a non-administrative command that can leave a lobby.
	server.Bind("LEAVE", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, false, true, packet)
		if halt {
			return
		}
		if admin {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "hosts may not use LEAVE, use CLOSE or TRANSFER instead",
			})
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Leave lobby
		lobby.Remove(peer)

		// Notify members
		for _, p := range server.Members[lobby] {
			if p == peer {
				continue
			}
			go p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "PEER_LEFT",
					TTL:    1,
				},
				Payload: peer.GetPeerID(),
			})
		}

		// Notify host
		host := server.Hosts[lobby]
		go host.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "PEER_LEFT",
				TTL:    1,
			},
			Payload: peer.GetPeerID(),
		})

		log.Printf("%s left %s", peer.GiveName(), lobby.ID)

		// Transition to ""
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "TRANSITION",
				TTL:    1,
			},
			Payload: "",
		})

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "LEAVE_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: lobby.ID,
		})
	})

	// CLOSE is an administrative command that can close a lobby.
	server.Bind("CLOSE", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin, halt := server.GetState(peer, true, true, packet)
		if halt {
			return
		}
		if !admin {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "WARNING",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: "members may not use CLOSE, use LEAVE instead",
			})
			return
		}

		// Obtain lock
		lobby.Lock()
		defer lobby.Unlock()

		// Notify all peers that the lobby has been closed
		for _, p := range server.Members[lobby] {
			p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "LOBBY_CLOSED",
					TTL:    1,
				},
				Payload: lobby.ID,
			})
			p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "TRANSITION",
					TTL:    1,
				},
				Payload: "",
			})
		}

		// Destroy the lobby
		delete(server.Lobbies, AnyToString(lobby.ID))
		delete(server.Members, lobby)
		delete(server.Hosts, lobby)
		log.Printf("%s closed %s", peer.GiveName(), lobby.ID)

		// Tell the host to TRANSITION to ""
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "TRANSITION",
				TTL:    1,
			},
			Payload: "",
		})

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "CLOSE_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: lobby.ID,
		})
	})

	// REGISTER programs a peer's preferred name, since discovery services require unique connection identifiers.
	server.Bind("REGISTER", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Read desired username
		var username string
		if err := json.Unmarshal([]byte(packet.Payload), &username); err != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: err.Error(),
			})
			peer.Close()
			return
		}

		// Obtain lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		// Check if the registry has a match
		if server.NameRegistry[username] != nil {
			peer.WriteBlocking(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "VIOLATION",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: fmt.Sprintf("Username %s is already in use", username),
			})
			peer.Close()
			return
		}

		// Register peer
		server.NameRegistry[username] = peer

		// Obtain lock and set name
		peer.KeyStore["name"] = username

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "REGISTER_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: username,
		})
	})

	// Stub handler that automatically declines incoming call requests
	server.Bind("CALL", func(peer *duplex.Peer, _ *duplex.RxPacket) {
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "DECLINE",
				TTL:    1,
			},
		})
	})

	return server
}

func (server *Instance) AutoRegister(peer *duplex.Peer, registry Registry) {
	username := peer.GetPeerID()
	log.Printf("Automatically registering %s", username)

	// Obtain lock
	server.Mutex.Lock()
	defer server.Mutex.Unlock()

	// Check if the registry has a match
	if registry[username] != nil {
		peer.WriteBlocking(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "VIOLATION",
				TTL:    1,
			},
			Payload: fmt.Sprintf("Automatic registration failure: Registry item %s is already in use", username),
		})
		peer.Close()
		return
	}

	// Register peer
	registry[username] = peer

	// Obtain lock and set name
	peer.KeyStore["name"] = username

	// Return success
	peer.Write(&duplex.TxPacket{
		Packet: duplex.Packet{
			Opcode: "AUTO_REGISTER",
			TTL:    1,
		},
		Payload: username,
	})
}

func (i *Instance) Multiquery(username string, peer *duplex.Peer, packet *duplex.RxPacket, streams Registry) {

	query_resolver := func(upstream *duplex.Peer) json.RawMessage {

		// Generate a unique listener UUID
		listener_id, _ := uuid.NewRandom()

		// Send request to the designation's discovery server
		reply := upstream.SendAndWaitForReply(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "QUERY",
				TTL:      1,
				Listener: listener_id.String(),
			},
			Payload: packet.Payload,
		})

		if reply.Opcode != "QUERY_ACK" {
			log.Printf("Multiquery error: %s replied with %v", upstream.GetPeerID(), reply)
			return nil
		}

		return reply.Payload
	}

	// Try to forward the request to available upstreams
	var wg sync.WaitGroup
	var valid_mux sync.Mutex
	valid_responses := make([]json.RawMessage, 0)
	for _, bridge := range streams {
		wg.Add(1)

		// Simultaneously ask all possible upstreams for the request
		go func(upstream *duplex.Peer) {
			defer wg.Done()
			if reply := query_resolver(upstream); reply != nil {
				valid_mux.Lock()
				defer valid_mux.Unlock()
				valid_responses = append(valid_responses, reply)
			}
		}(bridge)

	}

	// Wait for all simultaneous queries to resolve
	wg.Wait()

	// No valid replies present,
	if len(valid_responses) == 0 {
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "QUERY_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: PeerObject{
				Username: username,
				Online:   false,
			},
		})
		return
	}

	// If there are multiple options present, use the MULTI_QUERY_ACK response.
	if len(valid_responses) > 1 {
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "MULTI_QUERY_ACK",
				Listener: packet.Listener,
				TTL:      1,
			},
			Payload: valid_responses,
		})
		return
	}

	// Just use the normal QUERY_ACK response.
	peer.Write(&duplex.TxPacket{
		Packet: duplex.Packet{
			Opcode:   "QUERY_ACK",
			Listener: packet.Listener,
			TTL:      1,
		},
		Payload: valid_responses[0],
	})
}

// ResolvePeer is a function that resolves a peer based on a query string.
//
// @param query - The query string to search for.
// @param peer - The peer object that sent the query.
// @param packet - The Rx packet object that contains the query string.
//
// ResolvePeer will reply with a QUERY_ACK packet containing the resolved peer's information.
// If the query string does not match any peer in the instance's name registry, ResolvePeer will reply with a QUERY_ACK packet containing an empty name and false online status.
// If the query string matches a peer in the instance's name registry, ResolvePeer will reply with a QUERY_ACK packet containing the resolved peer's information.
func (i *Instance) ResolvePeer(username string, peer *duplex.Peer, packet *duplex.RxPacket) {

	// Obtain lock
	i.Mutex.Lock()

	target, locally_exists := i.NameRegistry[username]
	if !locally_exists {
		target, locally_exists = i.BridgeRegistry[username]
	}
	if !locally_exists {
		target, locally_exists = i.BridgeRegistry[username+"@"+i.Designation]
	}
	if !locally_exists {
		target, locally_exists = i.DiscoveryRegistry[username]
	}
	if !locally_exists {
		target, locally_exists = i.DiscoveryRegistry[username+"@"+i.Designation]
	}
	bridges := make(Registry)
	for k, v := range i.BridgeRegistry {
		bridges[k] = v
	}

	i.Mutex.Unlock()

	// Not found
	if !locally_exists {

		// No upstream bridges to query
		if len(bridges) == 0 {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "QUERY_ACK",
					Listener: packet.Listener,
					TTL:      1,
				},
				Payload: PeerObject{
					Username: username,
					Online:   false,
				},
			})

		} else {

			// Ask all connected bridges to try and find it
			i.Multiquery(username, peer, packet, bridges)

		}
		return
	}

	// found
	i.Mutex.Lock()
	lobby, isHost, _ := i.GetState(target, false, false, packet)
	var lobbyID string
	var isInLobby bool
	if lobby != nil {
		isInLobby = true
		lobbyID = AnyToString(lobby.ID)
	}
	i.Mutex.Unlock()

	response := &PeerObject{
		Online:        true,
		Username:      username,
		Designation:   i.Designation,
		InstanceID:    target.GetPeerID(),
		IsLobbyMember: isInLobby && !isHost,
		IsLobbyHost:   isInLobby && isHost,
		IsInLobby:     isInLobby,
		RTT:           target.RTT,
		IsLegacy:      false, // Always false if we're the Discovery server. Bridge servers will always reply with this set to true.
		IsRelayed:     false, // TODO: tweak this in case of a relay server is present. Bridge servers will always reply with this set to true.
		IsBridge:      target.IsBridge,
		IsDiscovery:   target.IsDiscovery,
	}

	if isInLobby {
		response.LobbyID = lobbyID
	}

	peer.Write(&duplex.TxPacket{
		Packet: duplex.Packet{
			Opcode:   "QUERY_ACK",
			Listener: packet.Listener,
			TTL:      1,
		},
		Payload: response,
	})
}

// GetState returns the current lobby and whether the peer is the host or not.
// If the peer is not the host and emit_warn is true, it will send a "UNAUTHORIZED" packet to the peer.
// If the peer is not in a lobby, it will send a "CONFIG_REQUIRED" packet to the peer.
// If halt_if_fail is true, it will halt execution of the opcode handler if any state checks fail.
func (i *Instance) GetState(p *duplex.Peer, emit_warn bool, halt_if_fail bool, packet *duplex.RxPacket) (*Lobby, bool, bool) {

	// Get current lobby
	lobby_id, ok := p.KeyStore["lobby"]
	if !ok {
		if emit_warn {
			p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "CONFIG_REQUIRED",
					Listener: packet.Listener,
					TTL:      1,
				},
			})
		}
		return nil, false, halt_if_fail
	}

	lobby := i.Lobbies[AnyToString(lobby_id)]
	if lobby == nil {
		if emit_warn {
			p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode:   "CONFIG_REQUIRED",
					Listener: packet.Listener,
					TTL:      1,
				},
			})
		}
		return nil, false, halt_if_fail
	}

	// Obtain lock
	lobby.Lock()
	defer lobby.Unlock()

	// Verify role as lobby host
	is_host := (p == i.Hosts[lobby])
	if !is_host && emit_warn {
		p.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode:   "UNAUTHORIZED",
				Listener: packet.Listener,
				TTL:      1,
			},
		})
		return lobby, false, halt_if_fail
	}

	return lobby, is_host, false
}

func ValidateAnyType(name any) error {
	switch name.(type) {
	case string, int, float64, bool:
		return nil // Valid types
	default:
		return fmt.Errorf("value must be a string, boolean, float, or int")
	}
}

func AnyToString(val any) string {
	if s, ok := val.(string); ok {
		return s
	}
	res, _ := json.Marshal(val)
	return string(res)
}

func StringToAny(val string) any {
	var res any
	_ = json.Unmarshal([]byte(val), &res)
	return res
}
