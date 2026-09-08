package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"bittorrent-client/internal/peer"
	"bittorrent-client/internal/torrent"
	"bittorrent-client/internal/tracker"
)

const (
	BlockSize           = 16 * 1024
	DefaultBlockTimeout = 30 * time.Second
)

type WorkerOptions struct {
	ClientConfig peer.ClientConfig
	BlockTimeout time.Duration
	OnProgress   func(completed, total int)
}

func StartWorker(
	ctx context.Context,
	peerAddress tracker.PeerAddress,
	meta *torrent.TorrentMeta,
	manager *PieceManager,
	peerID [20]byte,
	options WorkerOptions,
) error {
	client, err := peer.ConnectContext(ctx, peerAddress, meta.InfoHash, peerID, options.ClientConfig)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", peerAddress, err)
	}
	return RunClient(ctx, client, meta, manager, options)
}

// RunClient runs the download state machine over an already-handshaken client.
// It is separated from StartWorker to permit deterministic protocol tests.
func RunClient(
	ctx context.Context,
	client *peer.Client,
	meta *torrent.TorrentMeta,
	manager *PieceManager,
	options WorkerOptions,
) error {
	if client == nil || meta == nil || manager == nil {
		return errors.New("worker received a nil dependency")
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		_ = client.Close()
	}()
	messages, readErrors := client.ReadLoop(workerContext)
	if options.BlockTimeout <= 0 {
		options.BlockTimeout = DefaultBlockTimeout
	}
	if err := client.WriteMessage(peer.NewInterested()); err != nil {
		return fmt.Errorf("send interested: %w", err)
	}

	var (
		unchoked       bool
		currentPiece   *Piece
		received       []byte
		expectedBegin  int
		expectedLength int
		peerBitfield   = peer.NewBitfield(len(meta.Pieces))
	)
	blockTimer := time.NewTimer(time.Hour)
	if !blockTimer.Stop() {
		<-blockTimer.C
	}
	defer blockTimer.Stop()
	var blockDeadline <-chan time.Time
	stopBlockTimer := func() {
		if blockDeadline == nil {
			return
		}
		if !blockTimer.Stop() {
			select {
			case <-blockTimer.C:
			default:
			}
		}
		blockDeadline = nil
	}
	resetBlockTimer := func() {
		stopBlockTimer()
		blockTimer.Reset(options.BlockTimeout)
		blockDeadline = blockTimer.C
	}
	releaseCurrent := func() {
		if currentPiece != nil {
			manager.MarkFailed(currentPiece.Index)
		}
		currentPiece = nil
		received = nil
		expectedBegin = 0
		expectedLength = 0
		stopBlockTimer()
	}
	defer releaseCurrent()

	requestBlock := func() error {
		if currentPiece == nil {
			return errors.New("cannot request a block without a piece")
		}
		expectedBegin = len(received)
		remaining := currentPiece.Length - expectedBegin
		if remaining <= 0 {
			return errors.New("piece has no remaining data")
		}
		expectedLength = minInt(BlockSize, remaining)
		if err := client.WriteMessage(peer.NewRequest(currentPiece.Index, expectedBegin, expectedLength)); err != nil {
			return fmt.Errorf("request piece %d block %d: %w", currentPiece.Index, expectedBegin, err)
		}
		resetBlockTimer()
		return nil
	}
	requestNextPiece := func() error {
		if !unchoked || currentPiece != nil {
			return nil
		}
		currentPiece = manager.NextPieceForPeer(peerBitfield.Pieces)
		if currentPiece == nil {
			return nil
		}
		received = make([]byte, 0, currentPiece.Length)
		if err := requestBlock(); err != nil {
			releaseCurrent()
			return err
		}
		return nil
	}

	for messages != nil || readErrors != nil {
		updates := manager.Updates()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-blockDeadline:
			pieceIndex := currentPiece.Index
			releaseCurrent()
			return fmt.Errorf("timed out waiting for piece %d block", pieceIndex)
		case <-updates:
			if manager.IsComplete() {
				return nil
			}
			if err := requestNextPiece(); err != nil {
				return err
			}
		case err, ok := <-readErrors:
			if !ok {
				readErrors = nil
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read peer message: %w", err)
		case message, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			if message.ID == nil {
				continue
			}

			switch *message.ID {
			case peer.MsgBitfield:
				parsed, err := peer.ParseBitfield(message.Payload, len(meta.Pieces))
				if err != nil {
					return fmt.Errorf("invalid bitfield: %w", err)
				}
				peerBitfield = parsed
				if err := requestNextPiece(); err != nil {
					return err
				}

			case peer.MsgHave:
				index, err := peer.ParseHave(message.Payload)
				if err != nil {
					return err
				}
				if err := peerBitfield.SetPiece(index); err != nil {
					return err
				}
				if err := requestNextPiece(); err != nil {
					return err
				}

			case peer.MsgChoke:
				unchoked = false
				// A choke invalidates outstanding requests. Release the piece so
				// another peer can make progress.
				releaseCurrent()

			case peer.MsgUnchoke:
				unchoked = true
				if err := requestNextPiece(); err != nil {
					return err
				}

			case peer.MsgPiece:
				index, begin, block, err := peer.ParsePiece(message.Payload)
				if err != nil {
					return err
				}
				if currentPiece == nil || index != currentPiece.Index {
					continue // A late response for a cancelled request.
				}
				if begin != expectedBegin || len(block) != expectedLength || len(received)+len(block) > currentPiece.Length {
					releaseCurrent()
					return fmt.Errorf(
						"unexpected block for piece %d: offset=%d length=%d, expected offset=%d length=%d",
						index, begin, len(block), expectedBegin, expectedLength,
					)
				}
				stopBlockTimer()
				received = append(received, block...)
				if len(received) < currentPiece.Length {
					if err := requestBlock(); err != nil {
						releaseCurrent()
						return err
					}
					continue
				}

				pieceIndex := currentPiece.Index
				data := received
				currentPiece = nil
				received = nil
				if err := manager.CompletePiece(pieceIndex, data); err != nil {
					return fmt.Errorf("complete piece %d: %w", pieceIndex, err)
				}
				completed, total := manager.Progress()
				if options.OnProgress != nil {
					options.OnProgress(completed, total)
				}
				if manager.IsComplete() {
					return nil
				}
				if err := requestNextPiece(); err != nil {
					return err
				}
			}
		}
	}
	if manager.IsComplete() {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return io.EOF
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
