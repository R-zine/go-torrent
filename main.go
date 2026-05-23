package main

import (
	"fmt"
	"os"

	"bittorrent-client/internal/bencode"
	"bittorrent-client/internal/peer"
	"bittorrent-client/internal/torrent"
	"bittorrent-client/internal/tracker"
)

func main() {
	path := os.Args[1]

	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}

	parser := bencode.NewParser(data)

	root, err := parser.Parse()
	if err != nil {
		panic(err)
	}

	meta, err := torrent.ExtractMeta(root, data)
	if err != nil {
		panic(err)
	}

	peerID, err := peer.GeneratePeerID()
	if err != nil {
		panic(err)
	}

	response, err := tracker.Announce(meta, peerID, 6881)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Tracker interval: %d\n", response.Interval)
	fmt.Printf("Peers found: %d\n", len(response.Peers))

	var client *peer.Client

	for _, p := range response.Peers {
		fmt.Printf("Trying peer %s:%d\n", p.IP.String(), p.Port)

		c, err := peer.Connect(
			p,
			meta.InfoHash,
			peerID,
		)

		if err != nil {
			fmt.Printf("Connection failed: %v\n", err)
			continue
		}

		client = c

		fmt.Printf("Connected to peer %s:%d\n", p.IP.String(), p.Port)
		break
	}

	if client == nil {
		panic("failed to connect to any peer")
	}

	defer client.Close()

	messages := make(chan *peer.Message)
	errors := make(chan error)

	client.ReadLoop(messages, errors)

	// send interested immediately after connect/bitfield
	// (we don't strictly wait for bitfield for simplicity)
	_, _ = client.Conn.Write(peer.NewInterested().Serialize())

	var (

		pieceIndex   = 0
		received     []byte
		blockSize    = 16 * 1024
		expectedSize = meta.PieceLength
	)

	fmt.Println("Starting download loop...")

	for {
		select {

		case err := <-errors:
			panic(err)

		case msg := <-messages:

			if msg.ID == nil {
				continue
			}

			switch *msg.ID {

			case peer.MsgBitfield:
				fmt.Println("Received bitfield")

				// explicitly express interest
				_, _ = client.Conn.Write(peer.NewInterested().Serialize())

			case peer.MsgUnchoke:
				fmt.Println("Unchoked by peer")

				// first request
				req := peer.NewRequest(pieceIndex, 0, blockSize)
				_, _ = client.Conn.Write(req.Serialize())

			case peer.MsgPiece:
				index, begin, block, err := peer.ParsePiece(msg.Payload)
				if err != nil {
					fmt.Println("bad piece:", err)
					continue
				}

				if index != pieceIndex {
					continue
				}

				fmt.Printf("Got block offset=%d size=%d\n", begin, len(block))

				received = append(received, block...)

				// request next block
				if len(received) < expectedSize {
					nextBegin := len(received)

					req := peer.NewRequest(pieceIndex, nextBegin, blockSize)
					_, _ = client.Conn.Write(req.Serialize())
				} else {
					fmt.Println("Piece download complete (NOT verified yet)")
					fmt.Printf("Total bytes: %d\n", len(received))
					return
				}

			default:
				// TO-DO
			}
		}
	}
}