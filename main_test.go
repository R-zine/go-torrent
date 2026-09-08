package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bittorrent-client/internal/peer"
)

func TestRunDownloadsAndFinalizesFile(t *testing.T) {
	content := []byte("hello from a peer")
	peerListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peerListener.Close()
	peerErrors := make(chan error, 1)

	var trackerCalls atomic.Int32
	compactPeer := compactAddress(t, peerListener.Addr())
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		trackerCalls.Add(1)
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": compactPeer}))
	}))
	defer trackerServer.Close()
	metainfo, infoHash := singleFileMetainfo(trackerServer.URL+"?passkey=value", "result.bin", content, len(content))
	go serveSinglePiecePeer(peerListener, infoHash, content, peerErrors)

	root := t.TempDir()
	metainfoPath := filepath.Join(root, "test.torrent")
	if err := os.WriteFile(metainfoPath, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := run(ctx, metainfoPath, configuration); err != nil {
		t.Fatal(err)
	}
	if err := <-peerErrors; err != nil {
		t.Fatal(err)
	}
	completedPath := filepath.Join(configuration.OutputDirectory, "completed", "result.bin")
	got, err := os.ReadFile(completedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("completed content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(configuration.OutputDirectory, "in-progress", "result.bin.part")); !os.IsNotExist(err) {
		t.Fatalf("partial file remains: %v", err)
	}
	if trackerCalls.Load() < 2 {
		t.Fatalf("tracker calls = %d, want start and complete", trackerCalls.Load())
	}
}

func TestRunDownloadsAndFinalizesMultiFileTorrent(t *testing.T) {
	content := []byte("abcdef")
	peerListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peerListener.Close()
	peerErrors := make(chan error, 1)
	compactPeer := compactAddress(t, peerListener.Addr())
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": compactPeer}))
	}))
	defer trackerServer.Close()
	metainfo, infoHash := multiFileMetainfo(trackerServer.URL, "bundle", content)
	go serveSinglePiecePeer(peerListener, infoHash, content, peerErrors)

	root := t.TempDir()
	path := filepath.Join(root, "bundle.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	if err := run(context.Background(), path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := <-peerErrors; err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(configuration.OutputDirectory, "completed", "bundle", "one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(configuration.OutputDirectory, "completed", "bundle", "nested", "two"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "ab" || string(second) != "cdef" {
		t.Fatalf("completed files = %q and %q", first, second)
	}
}

func TestRunReturnsWhenTrackerHasNoPeers(t *testing.T) {
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": []byte{}}))
	}))
	defer trackerServer.Close()
	root := t.TempDir()
	metainfo, _ := singleFileMetainfo(trackerServer.URL, "no-peers.bin", []byte("data"), 4)
	path := filepath.Join(root, "no-peers.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := run(context.Background(), path, testAppConfig(filepath.Join(root, "downloads")))
	if err == nil || !strings.Contains(err.Error(), "no peers") {
		t.Fatalf("run() error = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("empty peer list did not fail promptly")
	}
}

func TestRunReturnsWhenAllPeersFail(t *testing.T) {
	// Port 1 on loopback is expected to reject connections immediately. The
	// dial timeout still provides a deterministic upper bound if it does not.
	compactPeer := []byte{127, 0, 0, 1, 0, 1}
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": compactPeer}))
	}))
	defer trackerServer.Close()
	root := t.TempDir()
	metainfo, _ := singleFileMetainfo(trackerServer.URL, "failed.bin", []byte("data"), 4)
	path := filepath.Join(root, "failed.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	configuration.PeerConfig.DialTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := run(ctx, path, configuration)
	if err == nil || !strings.Contains(err.Error(), "peers exhausted") {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunTriesPeersBeyondWorkerLimit(t *testing.T) {
	content := []byte("queued peer")
	peerListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peerListener.Close()
	peerErrors := make(chan error, 1)
	compactPeers := append([]byte{127, 0, 0, 1, 0, 1}, compactAddress(t, peerListener.Addr())...)
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": compactPeers}))
	}))
	defer trackerServer.Close()
	metainfo, infoHash := singleFileMetainfo(trackerServer.URL, "queued.bin", content, len(content))
	go serveSinglePiecePeer(peerListener, infoHash, content, peerErrors)

	root := t.TempDir()
	path := filepath.Join(root, "queued.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	configuration.MaxWorkers = 1
	if err := run(context.Background(), path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := <-peerErrors; err != nil {
		t.Fatal(err)
	}
}

func TestRunNoProgressTimeoutCancelsStalledPeer(t *testing.T) {
	content := []byte("stalled")
	peerListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peerListener.Close()
	peerErrors := make(chan error, 1)
	compactPeer := compactAddress(t, peerListener.Addr())
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": compactPeer}))
	}))
	defer trackerServer.Close()
	metainfo, infoHash := singleFileMetainfo(trackerServer.URL, "stalled.bin", content, len(content))
	go serveStallingPeer(peerListener, infoHash, peerErrors)

	root := t.TempDir()
	path := filepath.Join(root, "stalled.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	configuration.NoProgressLimit = 100 * time.Millisecond
	configuration.PeerConfig.ReadTimeout = 5 * time.Second
	start := time.Now()
	err = run(context.Background(), path, configuration)
	if err == nil || !strings.Contains(err.Error(), "no piece completed") {
		t.Fatalf("run() error = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("stalled worker was not cancelled promptly")
	}
	if err := <-peerErrors; err != nil {
		t.Fatal(err)
	}
}

func TestRunReannouncesAndAddsNewPeers(t *testing.T) {
	content := []byte("from reannounce")
	idleListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer idleListener.Close()
	goodListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer goodListener.Close()
	idleCompact := compactAddress(t, idleListener.Addr())
	goodCompact := compactAddress(t, goodListener.Addr())
	var calls atomic.Int32
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		peers := goodCompact
		if calls.Add(1) == 1 {
			peers = idleCompact
		}
		writer.Write(testEncode(map[string]any{"interval": int64(1), "peers": peers}))
	}))
	defer trackerServer.Close()
	metainfo, infoHash := singleFileMetainfo(trackerServer.URL, "reannounce.bin", content, len(content))
	idleErrors := make(chan error, 1)
	goodErrors := make(chan error, 1)
	go serveIdlePeer(idleListener, infoHash, idleErrors)
	go serveSinglePiecePeer(goodListener, infoHash, content, goodErrors)

	root := t.TempDir()
	path := filepath.Join(root, "reannounce.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	configuration.MaxWorkers = 2
	configuration.NoProgressLimit = 3 * time.Second
	configuration.PeerConfig.ReadTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := run(ctx, path, configuration); err != nil {
		t.Fatal(err)
	}
	if err := <-idleErrors; err != nil {
		t.Fatal(err)
	}
	if err := <-goodErrors; err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 3 {
		t.Fatalf("tracker calls = %d, want start, reannounce and complete", calls.Load())
	}
}

func TestRunFinalizesEmptyTorrentWithoutWorkers(t *testing.T) {
	trackerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(testEncode(map[string]any{"interval": int64(30), "peers": []byte{}}))
	}))
	defer trackerServer.Close()
	root := t.TempDir()
	metainfo, _ := singleFileMetainfo(trackerServer.URL, "empty.bin", nil, 16)
	path := filepath.Join(root, "empty.torrent")
	if err := os.WriteFile(path, metainfo, 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := testAppConfig(filepath.Join(root, "downloads"))
	if err := run(context.Background(), path, configuration); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(configuration.OutputDirectory, "completed", "empty.bin"))
	if err != nil || info.Size() != 0 {
		t.Fatalf("empty output = %v, %v", info, err)
	}
}

func TestResetTimer(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	resetTimer(timer, 10*time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(time.Second):
		t.Fatal("reset timer did not fire")
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	tests := []appConfig{
		{OutputDirectory: "", MaxWorkers: 1, NoProgressLimit: time.Second},
		{OutputDirectory: "out", MaxWorkers: 0, NoProgressLimit: time.Second},
		{OutputDirectory: "out", MaxWorkers: 1, NoProgressLimit: 0},
	}
	for _, configuration := range tests {
		if err := run(context.Background(), "not-read", configuration); err == nil {
			t.Errorf("configuration %+v unexpectedly accepted", configuration)
		}
	}
}

func testAppConfig(output string) appConfig {
	configuration := defaultAppConfig()
	configuration.OutputDirectory = output
	configuration.MaxWorkers = 2
	configuration.NoProgressLimit = time.Second
	configuration.PeerConfig.DialTimeout = time.Second
	configuration.PeerConfig.HandshakeTimeout = time.Second
	configuration.PeerConfig.ReadTimeout = time.Second
	configuration.PeerConfig.WriteTimeout = time.Second
	return configuration
}

func serveSinglePiecePeer(listener net.Listener, infoHash [20]byte, content []byte, result chan<- error) {
	fail := func(err error) {
		result <- err
	}
	connection, err := listener.Accept()
	if err != nil {
		fail(err)
		return
	}
	defer connection.Close()
	handshakeBytes := make([]byte, 68)
	if _, err := io.ReadFull(connection, handshakeBytes); err != nil {
		fail(err)
		return
	}
	handshake, err := peer.ParseHandshake(handshakeBytes)
	if err != nil || handshake.InfoHash != infoHash {
		fail(fmt.Errorf("invalid client handshake: %v", err))
		return
	}
	if _, err := connection.Write((&peer.Handshake{InfoHash: infoHash, PeerID: [20]byte{9}}).Serialize()); err != nil {
		fail(err)
		return
	}
	interested, err := peer.ReadMessage(connection)
	if err != nil || interested.ID == nil || *interested.ID != peer.MsgInterested {
		fail(fmt.Errorf("expected interested: %v", err))
		return
	}
	bitfieldID := peer.MsgBitfield
	if _, err := connection.Write((&peer.Message{ID: &bitfieldID, Payload: []byte{0x80}}).Serialize()); err != nil {
		fail(err)
		return
	}
	unchokeID := peer.MsgUnchoke
	if _, err := connection.Write((&peer.Message{ID: &unchokeID}).Serialize()); err != nil {
		fail(err)
		return
	}
	request, err := peer.ReadMessage(connection)
	if err != nil || request.ID == nil || *request.ID != peer.MsgRequest || len(request.Payload) != 12 {
		fail(fmt.Errorf("expected request: %v", err))
		return
	}
	length := int(binary.BigEndian.Uint32(request.Payload[8:12]))
	if length != len(content) {
		fail(fmt.Errorf("request length = %d", length))
		return
	}
	payload := make([]byte, 8+len(content))
	copy(payload[0:8], request.Payload[0:8])
	copy(payload[8:], content)
	pieceID := peer.MsgPiece
	if _, err := connection.Write((&peer.Message{ID: &pieceID, Payload: payload}).Serialize()); err != nil {
		fail(err)
		return
	}
	result <- nil
}

func serveStallingPeer(listener net.Listener, infoHash [20]byte, result chan<- error) {
	connection, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer connection.Close()
	handshakeBytes := make([]byte, 68)
	if _, err := io.ReadFull(connection, handshakeBytes); err != nil {
		result <- err
		return
	}
	if _, err := connection.Write((&peer.Handshake{InfoHash: infoHash, PeerID: [20]byte{7}}).Serialize()); err != nil {
		result <- err
		return
	}
	if _, err := peer.ReadMessage(connection); err != nil {
		result <- err
		return
	}
	bitfieldID := peer.MsgBitfield
	if _, err := connection.Write((&peer.Message{ID: &bitfieldID, Payload: []byte{0x80}}).Serialize()); err != nil {
		result <- err
		return
	}
	unchokeID := peer.MsgUnchoke
	if _, err := connection.Write((&peer.Message{ID: &unchokeID}).Serialize()); err != nil {
		result <- err
		return
	}
	if _, err := peer.ReadMessage(connection); err != nil {
		result <- err
		return
	}
	var oneByte [1]byte
	_, err = connection.Read(oneByte[:])
	if err == nil {
		result <- fmt.Errorf("stalled connection unexpectedly produced data")
		return
	}
	result <- nil
}

func serveIdlePeer(listener net.Listener, infoHash [20]byte, result chan<- error) {
	connection, err := listener.Accept()
	if err != nil {
		result <- err
		return
	}
	defer connection.Close()
	handshakeBytes := make([]byte, 68)
	if _, err := io.ReadFull(connection, handshakeBytes); err != nil {
		result <- err
		return
	}
	if _, err := connection.Write((&peer.Handshake{InfoHash: infoHash, PeerID: [20]byte{8}}).Serialize()); err != nil {
		result <- err
		return
	}
	if _, err := peer.ReadMessage(connection); err != nil {
		result <- err
		return
	}
	var oneByte [1]byte
	_, err = connection.Read(oneByte[:])
	if err == nil {
		result <- fmt.Errorf("idle connection unexpectedly produced data")
		return
	}
	result <- nil
}

func compactAddress(t *testing.T, address net.Addr) []byte {
	t.Helper()
	host, portString, err := net.SplitHostPort(address.String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := net.LookupPort("tcp", portString)
	if err != nil {
		t.Fatal(err)
	}
	result := append([]byte(nil), net.ParseIP(host).To4()...)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	return append(result, portBytes[:]...)
}

func singleFileMetainfo(trackerURL, name string, content []byte, pieceLength int) ([]byte, [20]byte) {
	var hashes []byte
	for offset := 0; offset < len(content); offset += pieceLength {
		end := offset + pieceLength
		if end > len(content) {
			end = len(content)
		}
		hash := sha1.Sum(content[offset:end])
		hashes = append(hashes, hash[:]...)
	}
	info := map[string]any{
		"length":       int64(len(content)),
		"name":         name,
		"piece length": int64(pieceLength),
		"pieces":       hashes,
	}
	encodedInfo := testEncode(info)
	return testEncode(map[string]any{"announce": trackerURL, "info": info}), sha1.Sum(encodedInfo)
}

func multiFileMetainfo(trackerURL, name string, content []byte) ([]byte, [20]byte) {
	hash := sha1.Sum(content)
	info := map[string]any{
		"files": []any{
			map[string]any{"length": int64(2), "path": []any{"one"}},
			map[string]any{"length": int64(len(content) - 2), "path": []any{"nested", "two"}},
		},
		"name":         name,
		"piece length": int64(len(content)),
		"pieces":       hash[:],
	}
	encodedInfo := testEncode(info)
	return testEncode(map[string]any{"announce": trackerURL, "info": info}), sha1.Sum(encodedInfo)
}

func testEncode(value any) []byte {
	var buffer bytes.Buffer
	var encode func(any)
	encode = func(value any) {
		switch value := value.(type) {
		case string:
			fmt.Fprintf(&buffer, "%d:%s", len(value), value)
		case []byte:
			fmt.Fprintf(&buffer, "%d:", len(value))
			buffer.Write(value)
		case int64:
			fmt.Fprintf(&buffer, "i%de", value)
		case []any:
			buffer.WriteByte('l')
			for _, item := range value {
				encode(item)
			}
			buffer.WriteByte('e')
		case map[string]any:
			buffer.WriteByte('d')
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				encode(key)
				encode(value[key])
			}
			buffer.WriteByte('e')
		default:
			panic(fmt.Sprintf("unsupported test type %T", value))
		}
	}
	encode(value)
	return buffer.Bytes()
}
