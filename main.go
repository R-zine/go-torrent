package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bittorrent-client/internal/bencode"
	"bittorrent-client/internal/download"
	"bittorrent-client/internal/peer"
	"bittorrent-client/internal/storage"
	"bittorrent-client/internal/torrent"
	"bittorrent-client/internal/tracker"
)

type appConfig struct {
	OutputDirectory string
	Port            uint16
	MaxWorkers      int
	NoProgressLimit time.Duration
	PeerConfig      peer.ClientConfig
}

func defaultAppConfig() appConfig {
	return appConfig{
		OutputDirectory: "./torrents",
		Port:            6881,
		MaxWorkers:      30,
		NoProgressLimit: 5 * time.Minute,
		PeerConfig:      peer.DefaultClientConfig(),
	}
}

func main() {
	configuration := defaultAppConfig()
	port := flag.Uint("port", uint(configuration.Port), "port announced to trackers")
	flag.StringVar(&configuration.OutputDirectory, "output", configuration.OutputDirectory, "download directory")
	flag.IntVar(&configuration.MaxWorkers, "workers", configuration.MaxWorkers, "maximum concurrent peer connections")
	flag.DurationVar(&configuration.NoProgressLimit, "idle-timeout", configuration.NoProgressLimit, "fail after this long without a completed piece")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] <torrent-file>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 || *port == 0 || *port > 65535 {
		flag.Usage()
		os.Exit(2)
	}
	configuration.Port = uint16(*port)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, flag.Arg(0), configuration); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, metainfoPath string, configuration appConfig) error {
	if configuration.OutputDirectory == "" {
		return errors.New("output directory must not be empty")
	}
	if configuration.MaxWorkers <= 0 {
		return errors.New("worker count must be positive")
	}
	if configuration.NoProgressLimit <= 0 {
		return errors.New("idle timeout must be positive")
	}
	rawData, err := os.ReadFile(metainfoPath)
	if err != nil {
		return fmt.Errorf("read metainfo: %w", err)
	}
	root, err := bencode.NewParser(rawData).Parse()
	if err != nil {
		return fmt.Errorf("parse metainfo: %w", err)
	}
	meta, err := torrent.ExtractMeta(root, rawData)
	if err != nil {
		return fmt.Errorf("extract metainfo: %w", err)
	}
	peerID, err := peer.GeneratePeerID()
	if err != nil {
		return fmt.Errorf("generate peer ID: %w", err)
	}

	fmt.Printf("Loaded torrent: %s\n", meta.Name)
	fmt.Printf("Total size: %d bytes\n", meta.Length)
	fmt.Printf("Pieces: %d\n", len(meta.Pieces))
	fmt.Printf("Peer ID: %s\n", peerID)

	store, err := storage.NewStorageAt(meta, configuration.OutputDirectory)
	if err != nil {
		return err
	}
	defer store.Close() // Complete makes this a harmless no-op.
	manager := download.NewPieceManager(meta, store)
	if err := manager.RestoreExisting(); err != nil {
		return err
	}
	completed, total := manager.Progress()
	if completed > 0 {
		fmt.Printf("Resumed verified pieces: %d/%d\n", completed, total)
	}

	trackerClient := tracker.NewClient()
	if !manager.IsComplete() {
		if err := downloadTorrent(ctx, trackerClient, meta, manager, peerID, configuration); err != nil {
			return err
		}
	}
	if err := manager.Finalize(); err != nil {
		return fmt.Errorf("finalize download: %w", err)
	}
	fmt.Printf("Download complete: %s\n", store.CompletedPath)

	// Completion notification is best-effort: downloaded data must not be
	// reported as failed solely because a tracker became unavailable.
	announceContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, announceErr := trackerClient.AnnounceFirst(announceContext, meta, peerID, tracker.AnnounceOptions{
		Port:       configuration.Port,
		Downloaded: meta.Length,
		Left:       0,
		Event:      "completed",
		NumWant:    -1,
	})
	if announceErr != nil {
		fmt.Fprintf(os.Stderr, "warning: completion announce failed: %v\n", announceErr)
	}
	return nil
}

type workerResult struct {
	address tracker.PeerAddress
	err     error
}

func downloadTorrent(
	ctx context.Context,
	trackerClient *tracker.Client,
	meta *torrent.TorrentMeta,
	manager *download.PieceManager,
	peerID [20]byte,
	configuration appConfig,
) error {
	downloadContext, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	completed, _ := manager.Progress()
	downloadedBytes := manager.CompletedBytes()
	response, err := trackerClient.AnnounceFirst(ctx, meta, peerID, tracker.AnnounceOptions{
		Port:       configuration.Port,
		Downloaded: downloadedBytes,
		Left:       meta.Length - downloadedBytes,
		Event:      "started",
		NumWant:    -1,
	})
	if err != nil {
		return err
	}
	if len(response.Peers) == 0 {
		return errors.New("tracker returned no peers")
	}
	fmt.Printf("Tracker interval: %d seconds\n", response.Interval)
	fmt.Printf("Peers found: %d\n", len(response.Peers))

	results := make(chan workerResult, configuration.MaxWorkers)
	active := make(map[string]struct{})
	attempts := make(map[string]int)
	queued := make(map[string]struct{})
	pending := make([]tracker.PeerAddress, 0, len(response.Peers))
	addPeers := func(peers []tracker.PeerAddress) {
		for _, address := range peers {
			key := address.String()
			if key == "" || attempts[key] >= 2 {
				continue
			}
			if _, exists := active[key]; exists {
				continue
			}
			if _, exists := queued[key]; exists {
				continue
			}
			queued[key] = struct{}{}
			pending = append(pending, address)
		}
	}
	addPeers(response.Peers)

	workerOptions := download.WorkerOptions{
		ClientConfig: configuration.PeerConfig,
		OnProgress: func(completed, total int) {
			fmt.Printf("Progress: %d/%d\n", completed, total)
		},
	}
	launchWorkers := func() {
		for len(active) < configuration.MaxWorkers && len(pending) > 0 && !manager.IsComplete() {
			address := pending[0]
			pending = pending[1:]
			key := address.String()
			delete(queued, key)
			active[key] = struct{}{}
			attempts[key]++
			go func() {
				err := download.StartWorker(downloadContext, address, meta, manager, peerID, workerOptions)
				results <- workerResult{address: address, err: err}
			}()
		}
	}
	launchWorkers()

	reannounceTimer := time.NewTimer(time.Duration(response.Interval) * time.Second)
	defer reannounceTimer.Stop()
	idleTimer := time.NewTimer(configuration.NoProgressLimit)
	defer idleTimer.Stop()
	lastCompleted := completed

	finishWorkers := func() {
		cancelWorkers()
		for len(active) > 0 {
			result := <-results
			delete(active, result.address.String())
		}
	}

	for {
		if manager.IsComplete() {
			finishWorkers()
			return nil
		}
		if len(active) == 0 && len(pending) == 0 {
			return fmt.Errorf("all discovered peers exhausted before completion (%d/%d pieces)", lastCompleted, len(meta.Pieces))
		}

		updates := manager.Updates()
		select {
		case <-ctx.Done():
			finishWorkers()
			return ctx.Err()

		case <-idleTimer.C:
			finishWorkers()
			if manager.IsComplete() {
				return nil
			}
			return fmt.Errorf("no piece completed for %s", configuration.NoProgressLimit)

		case <-updates:
			currentCompleted, _ := manager.Progress()
			if currentCompleted > lastCompleted {
				lastCompleted = currentCompleted
				resetTimer(idleTimer, configuration.NoProgressLimit)
			}
			launchWorkers()

		case result := <-results:
			delete(active, result.address.String())
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "peer %s failed: %v\n", result.address, result.err)
			}
			launchWorkers()

		case <-reannounceTimer.C:
			currentBytes := manager.CompletedBytes()
			updated, announceErr := trackerClient.AnnounceFirst(ctx, meta, peerID, tracker.AnnounceOptions{
				Port:       configuration.Port,
				Downloaded: currentBytes,
				Left:       meta.Length - currentBytes,
				NumWant:    -1,
			})
			if announceErr != nil {
				fmt.Fprintf(os.Stderr, "warning: tracker reannounce failed: %v\n", announceErr)
				resetTimer(reannounceTimer, 30*time.Second)
				continue
			}
			addPeers(updated.Peers)
			launchWorkers()
			resetTimer(reannounceTimer, time.Duration(updated.Interval)*time.Second)
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
