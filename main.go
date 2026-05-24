package main

import (
	"fmt"
	"os"
	"sync"

	"bittorrent-client/internal/bencode"
	"bittorrent-client/internal/download"
	"bittorrent-client/internal/peer"
	"bittorrent-client/internal/storage"
	"bittorrent-client/internal/torrent"
	"bittorrent-client/internal/tracker"
)

var once sync.Once

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run . <torrent-file>")
		os.Exit(1)
	}

	path := os.Args[1]

	// ======================================================
	// Read torrent file
	// ======================================================

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

	fmt.Printf("Loaded torrent: %s\n", meta.Name)
	fmt.Printf("Total size: %d bytes\n", meta.Length)
	fmt.Printf("Pieces: %d\n", len(meta.Pieces))

	// ======================================================
	// Create piece manager
	// ======================================================

	store, err := storage.NewStorage(meta)
if err != nil {
	panic(err)
}

	manager := download.NewPieceManager(meta, store)

	// ======================================================
	// Generate peer ID
	// ======================================================

	peerID, err := peer.GeneratePeerID()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Peer ID: %s\n", string(peerID[:]))

	// ======================================================
	// Announce to tracker
	// ======================================================

	response, err := tracker.Announce(meta, peerID, 6881)
	if err != nil {
		panic(err)
	}

	fmt.Printf("Tracker interval: %d\n", response.Interval)
	fmt.Printf("Peers found: %d\n", len(response.Peers))

	// ======================================================
	// Concurrency coordination
	// ======================================================

	done := make(chan struct{})

	var once sync.Once

	// ======================================================
	// Worker pool
	// ======================================================

	maxWorkers := 30

	for i, p := range response.Peers {

		if i >= maxWorkers {
			break
		}

		go download.StartWorker(
			p,
			meta,
			manager,
			peerID,
			done,
			&once,
		)
	}

	// ======================================================
	// Wait for completion
	// ======================================================

	<-done

	fmt.Println("All pieces downloaded")
}