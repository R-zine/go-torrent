package download

import (
	"fmt"
	"sync"

	"bittorrent-client/internal/peer"
	"bittorrent-client/internal/torrent"
	"bittorrent-client/internal/tracker"
)

const BlockSize = 16 * 1024

func StartWorker(
	peerAddr tracker.PeerAddress,
	meta *torrent.TorrentMeta,
	manager *PieceManager,
	peerID [20]byte,
	done chan struct{},
	once *sync.Once,
) {

	client, err := peer.Connect(
		peerAddr,
		meta.InfoHash,
		peerID,
	)
	if err != nil {
		fmt.Printf(
			"[worker %s:%d] connect failed: %v\n",
			peerAddr.IP.String(),
			peerAddr.Port,
			err,
		)
		return
	}

	defer client.Close()

	fmt.Printf(
		"[worker %s:%d] connected\n",
		peerAddr.IP.String(),
		peerAddr.Port,
	)

	messages := make(chan *peer.Message)
	errors := make(chan error)

	client.ReadLoop(messages, errors)

	// Express interest in peer pieces
	_, err = client.Conn.Write(
		peer.NewInterested().Serialize(),
	)
	if err != nil {
		fmt.Printf(
			"[worker %s:%d] failed sending interested: %v\n",
			peerAddr.IP.String(),
			peerAddr.Port,
			err,
		)
		return
	}

	var (
		unchoked bool

		currentPiece *Piece
		received     []byte

		peerBitfield *peer.Bitfield
	)

	requestNextPiece := func() bool {

		// Cannot request pieces until we know what peer owns
		if peerBitfield == nil {
			return false
		}

		currentPiece = manager.NextPieceForPeer(
			peerBitfield.Pieces,
		)

		if currentPiece == nil {
			return false
		}

		received = nil

		requestLength := BlockSize

		if currentPiece.Length < requestLength {
			requestLength = currentPiece.Length
		}

		req := peer.NewRequest(
			currentPiece.Index,
			0,
			requestLength,
		)

		_, err := client.Conn.Write(req.Serialize())
		if err != nil {

			fmt.Printf(
				"[worker %s:%d] failed requesting piece %d: %v\n",
				peerAddr.IP.String(),
				peerAddr.Port,
				currentPiece.Index,
				err,
			)

			manager.MarkFailed(currentPiece.Index)

			currentPiece = nil

			return false
		}

		fmt.Printf(
			"[worker %s:%d] downloading piece %d\n",
			peerAddr.IP.String(),
			peerAddr.Port,
			currentPiece.Index,
		)

		return true
	}

	for {

		select {

		case <-done:
			return

		case err := <-errors:

			if currentPiece != nil {
				manager.MarkFailed(currentPiece.Index)
			}

			fmt.Printf(
				"[worker %s:%d] error: %v\n",
				peerAddr.IP.String(),
				peerAddr.Port,
				err,
			)

			return

		case msg := <-messages:

			if msg.ID == nil {
				// keep-alive
				continue
			}

			switch *msg.ID {

			case peer.MsgBitfield:

				peerBitfield = peer.ParseBitfield(
					msg.Payload,
					len(meta.Pieces),
				)

				fmt.Printf(
					"[worker %s:%d] received bitfield\n",
					peerAddr.IP.String(),
					peerAddr.Port,
				)

				// If already unchoked, immediately request work
				if unchoked && currentPiece == nil {
					requestNextPiece()
				}

			case peer.MsgChoke:

				fmt.Printf(
					"[worker %s:%d] peer choked us\n",
					peerAddr.IP.String(),
					peerAddr.Port,
				)

				unchoked = false

			case peer.MsgUnchoke:

				fmt.Printf(
					"[worker %s:%d] peer unchoked us\n",
					peerAddr.IP.String(),
					peerAddr.Port,
				)

				unchoked = true

				if currentPiece == nil {
					requestNextPiece()
				}

			case peer.MsgPiece:

				index, begin, block, err := peer.ParsePiece(msg.Payload)
				if err != nil {
					fmt.Printf(
						"[worker %s:%d] invalid piece payload: %v\n",
						peerAddr.IP.String(),
						peerAddr.Port,
						err,
					)
					continue
				}

				if currentPiece == nil {
					continue
				}

				if index != currentPiece.Index {
					continue
				}

				fmt.Printf(
					"[worker %s:%d] received block piece=%d offset=%d size=%d\n",
					peerAddr.IP.String(),
					peerAddr.Port,
					index,
					begin,
					len(block),
				)

				// NOTE:
				// This still assumes in-order block delivery.
				received = append(received, block...)

				// ==================================================
				// Piece incomplete -> request next block
				// ==================================================

				if len(received) < currentPiece.Length {

					nextBegin := len(received)

					remaining := currentPiece.Length - nextBegin

					requestLength := BlockSize

					if remaining < requestLength {
						requestLength = remaining
					}

					req := peer.NewRequest(
						currentPiece.Index,
						nextBegin,
						requestLength,
					)

					_, err := client.Conn.Write(req.Serialize())
					if err != nil {

						fmt.Printf(
							"[worker %s:%d] failed requesting next block: %v\n",
							peerAddr.IP.String(),
							peerAddr.Port,
							err,
						)

						manager.MarkFailed(currentPiece.Index)

						currentPiece = nil

						continue
					}

				} else {

					// ==================================================
					// Piece complete
					// ==================================================

					fmt.Printf(
						"[worker %s:%d] verifying piece %d\n",
						peerAddr.IP.String(),
						peerAddr.Port,
						currentPiece.Index,
					)

					err := manager.CompletePiece(
						currentPiece.Index,
						received,
					)

					if err == nil {

						completed, total := manager.Progress()

						fmt.Printf(
							"Progress: %d/%d\n",
							completed,
							total,
						)

					} else {

						fmt.Printf(
							"[worker %s:%d] piece %d failed verification: %v\n",
							peerAddr.IP.String(),
							peerAddr.Port,
							currentPiece.Index,
							err,
						)

						manager.MarkFailed(currentPiece.Index)
					}

					currentPiece = nil
					received = nil

					// ==================================================
					// Torrent complete
					// ==================================================

					if manager.IsComplete() {

						fmt.Println("Download complete!")

						once.Do(func() {
							close(done)
						})

						return
					}

					// ==================================================
					// Continue downloading
					// ==================================================

					if unchoked {
						requestNextPiece()
					}
				}

			default:
				// Ignore all other messages for now
			}
		}
	}
}