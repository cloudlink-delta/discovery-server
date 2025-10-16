package server

import (
	"log"
	"strings"

	"github.com/goccy/go-json"
)

func (conn *Client) HandlePacket(reader *RxPacket) {
	switch reader.Opcode {
	case "NEGOTIATE":
		var arguments NegotiationArgs
		err := json.Unmarshal(reader.Payload, &arguments)
		if err != nil {
			log.Println(err)
			return
		}

		log.Printf("%s using dialect revision %d on %s (v%d.%d.%d)\n",
			conn.GiveName(),
			arguments.SpecVersion,
			arguments.Version.Type,
			arguments.Version.Major,
			arguments.Version.Minor,
			arguments.Version.Patch,
		)

		var advertised_features []string
		if arguments.IsBridge {
			advertised_features = append(advertised_features, "bridge")
			// TODO: request list of bridged peers and their CL versions/dialects for translation
		}
		if arguments.IsRelay {
			advertised_features = append(advertised_features, "relay")
			// TODO: reconfigure routes to use relay for broadcasts
		}
		if arguments.IsDiscovery {
			advertised_features = append(advertised_features, "discovery")
			// TODO: verify authenticity of discovery server
		}

		if len(advertised_features) > 0 {
			log.Printf("%s advertises the following features: %v", conn.GiveName(), strings.Join(advertised_features, ", "))
		}

		// Reply with our capabilities and version
		conn.Write(&TxPacket{
			Packet: Packet{
				Opcode: "NEGOTIATE",
			},
			Payload: NegotiationArgs{
				Version: VersionArgs{
					Type:  "Go", // Do not change this
					Major: 1,
					Minor: 0,
					Patch: 0,
				},
				SpecVersion: 0,
				Plugins:     []string{},
				IsBridge:    false,
				IsRelay:     false,
				IsDiscovery: true,
			},
		})

	default:

		// Just echo back for the moment
		conn.Write(&TxPacket{
			Packet: Packet{
				Opcode: reader.Opcode,
			},
			Payload: reader.Payload,
		})
	}
}
