package peer

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"bittorrent-client/internal/tracker"
)

func TestConnectContextPerformsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	host, portString, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portString)
	address := tracker.PeerAddress{IP: net.ParseIP(host), Port: uint16(port)}
	infoHash := [20]byte{1, 2, 3}
	serverError := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverError <- err
			return
		}
		defer connection.Close()
		request := make([]byte, 68)
		if _, err := io.ReadFull(connection, request); err != nil {
			serverError <- err
			return
		}
		handshake, err := ParseHandshake(request)
		if err != nil {
			serverError <- err
			return
		}
		response := &Handshake{InfoHash: handshake.InfoHash, PeerID: [20]byte{9}}
		serverError <- writeFull(connection, response.Serialize())
	}()

	config := DefaultClientConfig()
	config.DialTimeout = time.Second
	config.HandshakeTimeout = time.Second
	client, err := ConnectContext(context.Background(), address, infoHash, [20]byte{4}, config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}

func TestConnectContextRejectsInfoHashMismatch(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		request := make([]byte, 68)
		_, _ = io.ReadFull(connection, request)
		_ = writeFull(connection, (&Handshake{InfoHash: [20]byte{99}}).Serialize())
	}()
	config := DefaultClientConfig()
	config.HandshakeTimeout = time.Second
	_, err = ConnectContext(context.Background(), tracker.PeerAddress{
		IP: net.ParseIP("127.0.0.1"), Port: uint16(port),
	}, [20]byte{1}, [20]byte{2}, config)
	if err == nil || !strings.Contains(err.Error(), "info hash mismatch") {
		t.Fatalf("ConnectContext() error = %v", err)
	}
}

func TestHandshakeDeadline(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	config := DefaultClientConfig()
	config.HandshakeTimeout = 20 * time.Millisecond
	client := NewClient(clientConnection, tracker.PeerAddress{}, config)
	defer client.Close()
	if err := client.performHandshake([20]byte{}, [20]byte{}); err == nil {
		t.Fatal("stalled handshake unexpectedly succeeded")
	}
}

func TestReadLoopReceivesMessageAndCancelsPromptly(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	defer serverConnection.Close()
	config := DefaultClientConfig()
	config.ReadTimeout = time.Second
	client := NewClient(clientConnection, tracker.PeerAddress{}, config)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	messages, errorsChannel := client.ReadLoop(ctx)

	go func() {
		_ = writeFull(serverConnection, NewHave(3).Serialize())
	}()
	select {
	case message := <-messages:
		if message == nil || message.ID == nil || *message.ID != MsgHave {
			t.Fatalf("message = %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("message was not delivered")
	}
	cancel()
	select {
	case _, ok := <-errorsChannel:
		if ok {
			t.Fatal("cancellation should not be reported as a read error")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("ReadLoop did not stop promptly")
	}
}

func TestWriteMessageAndCloseAreSafe(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	client := NewClient(clientConnection, tracker.PeerAddress{}, DefaultClientConfig())
	read := make(chan *Message, 1)
	go func() {
		message, _ := ReadMessage(serverConnection)
		read <- message
	}()
	if err := client.WriteMessage(NewInterested()); err != nil {
		t.Fatal(err)
	}
	if message := <-read; message == nil || message.ID == nil || *message.ID != MsgInterested {
		t.Fatalf("message = %#v", message)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	_ = serverConnection.Close()
}
