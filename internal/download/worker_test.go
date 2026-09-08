package download

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"bittorrent-client/internal/peer"
	"bittorrent-client/internal/storage"
	"bittorrent-client/internal/torrent"
	"bittorrent-client/internal/tracker"
)

func TestRunClientDownloadsMultipleBlocks(t *testing.T) {
	content := make([]byte, BlockSize+3)
	for i := range content {
		content[i] = byte(i % 251)
	}
	meta := testMeta(content, len(content))
	manager, store := workerManager(t, meta)
	defer store.Close()
	clientConnection, serverConnection := net.Pipe()
	client := testPeerClient(clientConnection)
	serverErrors := make(chan error, 1)
	go func() {
		defer serverConnection.Close()
		if err := expectMessageID(serverConnection, peer.MsgInterested); err != nil {
			serverErrors <- err
			return
		}
		if err := sendBitfield(serverConnection, []byte{0x80}); err != nil {
			serverErrors <- err
			return
		}
		if err := sendID(serverConnection, peer.MsgUnchoke, nil); err != nil {
			serverErrors <- err
			return
		}
		for offset := 0; offset < len(content); {
			request, err := peer.ReadMessage(serverConnection)
			if err != nil {
				serverErrors <- err
				return
			}
			index, begin, length, err := parseRequest(request)
			if err != nil || index != 0 || begin != offset {
				serverErrors <- errors.New("unexpected request")
				return
			}
			payload := make([]byte, 8+length)
			binary.BigEndian.PutUint32(payload[0:4], uint32(index))
			binary.BigEndian.PutUint32(payload[4:8], uint32(begin))
			copy(payload[8:], content[begin:begin+length])
			if err := sendID(serverConnection, peer.MsgPiece, payload); err != nil {
				serverErrors <- err
				return
			}
			offset += length
		}
		serverErrors <- nil
	}()

	progressCalls := 0
	err := RunClient(context.Background(), client, meta, manager, WorkerOptions{
		OnProgress: func(completed, total int) {
			progressCalls++
			if completed != 1 || total != 1 {
				t.Errorf("progress = %d/%d", completed, total)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
	if !manager.IsComplete() || progressCalls != 1 {
		t.Fatalf("complete=%v progress calls=%d", manager.IsComplete(), progressCalls)
	}
}

func TestRunClientUsesHaveWithoutBitfield(t *testing.T) {
	content := []byte("data")
	meta := testMeta(content, 4)
	manager, store := workerManager(t, meta)
	defer store.Close()
	clientConnection, serverConnection := net.Pipe()
	client := testPeerClient(clientConnection)
	serverErrors := make(chan error, 1)
	go func() {
		defer serverConnection.Close()
		if err := expectMessageID(serverConnection, peer.MsgInterested); err != nil {
			serverErrors <- err
			return
		}
		if err := writeMessage(serverConnection, peer.NewHave(0)); err != nil {
			serverErrors <- err
			return
		}
		if err := sendID(serverConnection, peer.MsgUnchoke, nil); err != nil {
			serverErrors <- err
			return
		}
		request, err := peer.ReadMessage(serverConnection)
		if err != nil {
			serverErrors <- err
			return
		}
		_, begin, length, err := parseRequest(request)
		if err != nil {
			serverErrors <- err
			return
		}
		payload := make([]byte, 8+length)
		copy(payload[8:], content)
		binary.BigEndian.PutUint32(payload[4:8], uint32(begin))
		serverErrors <- sendID(serverConnection, peer.MsgPiece, payload)
	}()
	if err := RunClient(context.Background(), client, meta, manager, WorkerOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
	if !manager.IsComplete() {
		t.Fatal("have-only peer did not complete piece")
	}
}

func TestRunClientReleasesPieceWhenChoked(t *testing.T) {
	meta := testMeta([]byte("data"), 4)
	manager, store := workerManager(t, meta)
	defer store.Close()
	clientConnection, serverConnection := net.Pipe()
	client := testPeerClient(clientConnection)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chokeSent := make(chan struct{})
	go func() {
		defer serverConnection.Close()
		_ = expectMessageID(serverConnection, peer.MsgInterested)
		_ = sendBitfield(serverConnection, []byte{0x80})
		_ = sendID(serverConnection, peer.MsgUnchoke, nil)
		_, _ = peer.ReadMessage(serverConnection)
		_ = sendID(serverConnection, peer.MsgChoke, nil)
		close(chokeSent)
		<-ctx.Done()
	}()
	result := make(chan error, 1)
	go func() { result <- RunClient(ctx, client, meta, manager, WorkerOptions{}) }()
	select {
	case <-chokeSent:
	case <-time.After(time.Second):
		t.Fatal("server did not send choke")
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, _ := manager.State(0)
		if state == PieceMissing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("piece state = %v after choke", state)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunClient() error = %v", err)
	}
}

func TestRunClientRejectsUnexpectedBlockOffset(t *testing.T) {
	meta := testMeta([]byte("data"), 4)
	manager, store := workerManager(t, meta)
	defer store.Close()
	clientConnection, serverConnection := net.Pipe()
	client := testPeerClient(clientConnection)
	go func() {
		defer serverConnection.Close()
		_ = expectMessageID(serverConnection, peer.MsgInterested)
		_ = sendBitfield(serverConnection, []byte{0x80})
		_ = sendID(serverConnection, peer.MsgUnchoke, nil)
		_, _ = peer.ReadMessage(serverConnection)
		payload := make([]byte, 12)
		binary.BigEndian.PutUint32(payload[4:8], 1)
		copy(payload[8:], "data")
		_ = sendID(serverConnection, peer.MsgPiece, payload)
	}()
	err := RunClient(context.Background(), client, meta, manager, WorkerOptions{})
	if err == nil || !strings.Contains(err.Error(), "unexpected block") {
		t.Fatalf("RunClient() error = %v", err)
	}
	state, _ := manager.State(0)
	if state != PieceMissing {
		t.Fatalf("piece state = %v", state)
	}
}

func TestRunClientCancellationReleasesPiece(t *testing.T) {
	meta := testMeta([]byte("data"), 4)
	manager, store := workerManager(t, meta)
	defer store.Close()
	clientConnection, serverConnection := net.Pipe()
	client := testPeerClient(clientConnection)
	ctx, cancel := context.WithCancel(context.Background())
	requestSeen := make(chan struct{})
	go func() {
		defer serverConnection.Close()
		_ = expectMessageID(serverConnection, peer.MsgInterested)
		_ = sendBitfield(serverConnection, []byte{0x80})
		_ = sendID(serverConnection, peer.MsgUnchoke, nil)
		_, _ = peer.ReadMessage(serverConnection)
		close(requestSeen)
		<-ctx.Done()
	}()
	result := make(chan error, 1)
	go func() { result <- RunClient(ctx, client, meta, manager, WorkerOptions{}) }()
	select {
	case <-requestSeen:
	case <-time.After(time.Second):
		t.Fatal("worker never requested a piece")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunClient() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
	state, _ := manager.State(0)
	if state != PieceMissing {
		t.Fatalf("piece state = %v", state)
	}
}

func TestRunClientBlockTimeoutReleasesPiece(t *testing.T) {
	meta := testMeta([]byte("data"), 4)
	manager, store := workerManager(t, meta)
	defer store.Close()
	clientConnection, serverConnection := net.Pipe()
	client := testPeerClient(clientConnection)
	requestSeen := make(chan struct{})
	go func() {
		defer serverConnection.Close()
		_ = expectMessageID(serverConnection, peer.MsgInterested)
		_ = sendBitfield(serverConnection, []byte{0x80})
		_ = sendID(serverConnection, peer.MsgUnchoke, nil)
		_, _ = peer.ReadMessage(serverConnection)
		close(requestSeen)
		// Keep the connection alive beyond the block deadline. Closing it only
		// after the client returns avoids making EOF the cause of failure.
		time.Sleep(100 * time.Millisecond)
	}()
	start := time.Now()
	err := RunClient(context.Background(), client, meta, manager, WorkerOptions{BlockTimeout: 20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out waiting") {
		t.Fatalf("RunClient() error = %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("block timeout did not fire promptly")
	}
	select {
	case <-requestSeen:
	default:
		t.Fatal("piece request was not observed")
	}
	state, _ := manager.State(0)
	if state != PieceMissing {
		t.Fatalf("piece state = %v", state)
	}
}

func workerManager(t *testing.T, meta *torrent.TorrentMeta) (*PieceManager, *storage.Storage) {
	t.Helper()
	store, err := storage.NewStorageAt(meta, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewPieceManager(meta, store), store
}

func testPeerClient(connection net.Conn) *peer.Client {
	config := peer.DefaultClientConfig()
	config.ReadTimeout = time.Second
	config.WriteTimeout = time.Second
	return peer.NewClient(connection, tracker.PeerAddress{}, config)
}

func expectMessageID(reader io.Reader, expected byte) error {
	message, err := peer.ReadMessage(reader)
	if err != nil {
		return err
	}
	if message.ID == nil || *message.ID != expected {
		return errors.New("unexpected message ID")
	}
	return nil
}

func writeMessage(writer io.Writer, message *peer.Message) error {
	data := message.Serialize()
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func sendID(writer io.Writer, id byte, payload []byte) error {
	return writeMessage(writer, &peer.Message{ID: &id, Payload: payload})
}

func sendBitfield(writer io.Writer, payload []byte) error {
	return sendID(writer, peer.MsgBitfield, payload)
}

func parseRequest(message *peer.Message) (index, begin, length int, err error) {
	if message.ID == nil || *message.ID != peer.MsgRequest || len(message.Payload) != 12 {
		return 0, 0, 0, errors.New("invalid request message")
	}
	return int(binary.BigEndian.Uint32(message.Payload[0:4])),
		int(binary.BigEndian.Uint32(message.Payload[4:8])),
		int(binary.BigEndian.Uint32(message.Payload[8:12])), nil
}
