package server

import (
	"fmt"
	"log"
	"slices"
	"sync"

	"github.com/goccy/go-json"

	"github.com/cloudlink-delta/duplex"
)

// Define a struct to hold lobby data
type Lobby struct {
	ID           any        `json:"lobby_id"`      // ID of the lobby
	Host         string     `json:"host"`          // Host of the lobby
	CurrentPeers int64      `json:"current_peers"` // Number of peers currently in the lobby
	MaxPeers     int64      `json:"max_peers"`     // Maximum number of peers allowed in the lobby
	Hidden       bool       `json:"hidden"`        // Whether the lobby is hidden
	Locked       bool       `json:"locked"`        // Whether the lobby is locked
	Metadata     any        `json:"metadata"`      // Arbitrary, user-defined storage
	Password     string     `json:"-"`             // Password for the lobby
	Instance     *Instance  `json:"-"`             // Pointer to the instance
	*sync.Mutex  `json:"-"` // Mutex for thread safety
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
	peers := l.Instance.Members[l]
	l.Instance.Members[l] = slices.Delete(peers, slices.Index(peers, peer), 1)
}

func (l *Lobby) PrecomputeTasks() {
	l.GetHost()
	l.ComputeCount()
}

func (l *Lobby) GetHost() {
	if len(l.Instance.Members[l]) > 0 {
		l.Host = l.Instance.Members[l][0].GetPeerID()
	} else {
		l.Host = ""
	}
}

func (l *Lobby) ComputeCount() {
	l.CurrentPeers = int64(len(l.Instance.Members[l]))
}

// Define Discovery server
type Instance struct {
	Lobbies Lobbies
	Hosts   Hosts
	Members Peers
	Mutex   *sync.Mutex
	*duplex.Instance
}

func New(ID string) *Instance {

	// Initialize duplex instance
	server := &Instance{
		Instance: duplex.New(ID),
		Lobbies:  make(Lobbies),
		Hosts:    make(Hosts),
		Members:  make(Peers),
		Mutex:    &sync.Mutex{},
	}
	server.IsDiscovery = true

	server.OnOpen = func(peer *duplex.Peer) {

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
	}

	server.OnClose = func(peer *duplex.Peer) {
		// Read state
		lobby, host := server.GetState(peer, false)

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

				// TODO: tell peers they are now the host

				log.Printf("made %s the new host of lobby %v", new_host.GetPeerID(), lobby.ID)

			} else {

				// Destroy the lobby if there are no more members
				delete(server.Lobbies, AnyToString(lobby.ID))
				delete(server.Members, lobby)

				// TODO: tell peers the lobby has been destroyed

				log.Printf("destroyed lobby %v since it was empty", lobby.ID)
			}

		} else {
			// Perform member actions

			// Remove from members
			lobby.Remove(peer)

			log.Printf("removed %s from lobby %v", peer.GetPeerID(), lobby.ID)
		}
	}

	// Bind opcode handlers
	server.Bind("LOBBY_INFO", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Unmarshal target
		var target any
		if err := json.Unmarshal(packet.Payload, &target); err != nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "WARNING",
					TTL:    1,
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
					Opcode: "WARNING",
					TTL:    1,
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
				Opcode: "LOBBY_INFO",
				TTL:    1,
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
			return
		}

		// Obtain lock
		server.Mutex.Lock()
		defer server.Mutex.Unlock()

		// Check if a lobby entry exists
		if _, exists := server.Lobbies[AnyToString(args.LobbyID)]; exists {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "LOBBY_EXISTS",
					TTL:    1,
				},
			})
			return
		}

		// TODO: validate arguments (metadata needs to be marshalable, max peer count can't be negative or zero, etc.)

		// Validate type of ID
		if err := ValidateAnyType(args.LobbyID); err != nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "WARNING",
					TTL:    1,
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
					Opcode: "WARNING",
					TTL:    1,
				},
				Payload: "max_peers cannot be negative",
			})
			return
		}

		// Create lobby
		lobby := &Lobby{
			ID:       args.LobbyID,
			MaxPeers: *args.MaxPeers,
			Locked:   args.Locked,
			Hidden:   args.Hidden,
			Password: args.Password,
			Metadata: args.Metadata,
			Mutex:    &sync.Mutex{},
			Instance: server,
		}

		// Add lobby to server
		server.Lobbies[AnyToString(args.LobbyID)] = lobby
		server.Hosts[lobby] = peer

		// Set key
		peer.KeyStore["lobby"] = lobby

		// Log
		log.Printf("%s created %v", peer.GiveName(), args.LobbyID)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "CONFIG_HOST_ACK",
				TTL:    1,
			},
			Payload: args.LobbyID,
		})

		// If hidden, do not broadcast
		if args.Hidden {
			return
		}

		// Tell all peers about the new lobby (except the host)
		server.Broadcast(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "NEW_LOBBY",
				TTL:    1,
			},
			Payload: lobby,
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
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "VIOLATION",
					TTL:    1,
				},
				Payload: err.Error(),
			})
			return
		}

		// Check if a lobby entry exists
		if _, exists := server.Lobbies[AnyToString(args.LobbyID)]; !exists {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "LOBBY_NOTFOUND",
					TTL:    1,
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
					Opcode: "LOBBY_LOCKED",
					TTL:    1,
				},
			})
			return
		}

		// Validate current count and state
		if lobby.MaxPeers != -1 && int64(len(server.Members[lobby])) >= lobby.MaxPeers {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "LOBBY_FULL",
					TTL:    1,
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
						Opcode: "PASSWORD_REQUIRED",
						TTL:    1,
					},
				})
				return

			} else if args.Password != lobby.Password {

				// Password is incorrect
				peer.Write(&duplex.TxPacket{
					Packet: duplex.Packet{
						Opcode: "PASSWORD_FAIL",
						TTL:    1,
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
		peer.KeyStore["lobby"] = lobby

		// Log
		log.Printf("%s joined %v", peer.GiveName(), lobby)

		// Return success
		peer.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "CONFIG_PEER_ACK",
				TTL:    1,
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
	server.Bind("LOBBY_LIST", func(peer *duplex.Peer, _ *duplex.RxPacket) {

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
	})

	// LOCK is an administrative command that can lock access to a lobby.
	server.Bind("LOCK", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Get current lobby
		lobby, admin := server.GetState(peer, true)
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
				Opcode: "LOCK_ACK",
				TTL:    1,
			},
		})
	})

	// UNLOCK is an administrative command that can unlock access to a lobby.
	server.Bind("UNLOCK", func(peer *duplex.Peer, packet *duplex.RxPacket) {

		// Get current lobby
		lobby, admin := server.GetState(peer, true)
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
				Opcode: "UNLOCK_ACK",
				TTL:    1,
			},
		})
	})

	// SIZE is an adminstrative command that can change the max player count of a lobby.
	server.Bind("SIZE", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin := server.GetState(peer, true)
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
					Opcode: "WARNING",
					TTL:    1,
				},
				Payload: "payload: must be an integer",
			})
			return
		}

		// Read new value
		var new_count int64
		if err := json.Unmarshal(packet.Payload, &new_count); err != nil {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "WARNING",
					TTL:    1,
				},
				Payload: err.Error(),
			})
			return
		}

		if new_count != -1 && new_count < 0 {
			peer.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "WARNING",
					TTL:    1,
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
				Opcode: "SIZE_ACK",
				TTL:    1,
			},
		})
	})

	// KICK is an administrative command that can remove a peer from a lobby.
	server.Bind("KICK", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// TODO: implement
	})

	// HIDE is an administrative command that can prevent a lobby from being shown in the lobby list or broadcasts.
	server.Bind("HIDE", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin := server.GetState(peer, true)
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
				Opcode: "HIDE_ACK",
				TTL:    1,
			},
		})
	})

	// SHOW is an administrative command that can allow a lobby from being shown in the lobby list list or broadcasts.
	server.Bind("SHOW", func(peer *duplex.Peer, packet *duplex.RxPacket) {
		// Get current lobby
		lobby, admin := server.GetState(peer, true)
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
				Opcode: "SHOW_ACK",
				TTL:    1,
			},
		})
	})

	return server
}

// GetState returns the current lobby and whether the peer is the host or not.
// If the peer is not the host and emit_warn is true, it will send a "UNAUTHORIZED" packet to the peer.
// If the peer is not in a lobby, it will send a "CONFIG_REQUIRED" packet to the peer.
func (i *Instance) GetState(p *duplex.Peer, emit_warn bool) (*Lobby, bool) {

	// Get current lobby
	lobby, ok := p.KeyStore["lobby"].(*Lobby)
	if lobby == nil {
		if emit_warn {
			p.Write(&duplex.TxPacket{
				Packet: duplex.Packet{
					Opcode: "CONFIG_REQUIRED",
					TTL:    1,
				},
			})
		}
		return nil, false
	} else if !ok {
		panic("lobby: unexpected type assertion failure")
	}

	// Obtain lock
	lobby.Lock()
	defer lobby.Unlock()

	// Verify role as lobby host
	is_host := (p == i.Hosts[lobby])
	if !is_host && emit_warn {
		p.Write(&duplex.TxPacket{
			Packet: duplex.Packet{
				Opcode: "UNAUTHORIZED",
				TTL:    1,
			},
		})
	}

	return lobby, is_host
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
	res, _ := json.Marshal(val)
	return string(res)
}

func StringToAny(val string) any {
	var res any
	_ = json.Unmarshal([]byte(val), &res)
	return res
}
