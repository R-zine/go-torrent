package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"bittorrent-client/internal/tracker"
)

type ClientConfig struct {
	DialTimeout      time.Duration
	HandshakeTimeout time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	MaxMessageSize   uint32
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		DialTimeout:      5 * time.Second,
		HandshakeTimeout: 10 * time.Second,
		ReadTimeout:      90 * time.Second,
		WriteTimeout:     15 * time.Second,
		MaxMessageSize:   DefaultMaxMessageSize,
	}
}

type Client struct {
	Conn net.Conn
	Peer tracker.PeerAddress

	config    ClientConfig
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func Connect(address tracker.PeerAddress, infoHash [20]byte, peerID [20]byte) (*Client, error) {
	return ConnectContext(context.Background(), address, infoHash, peerID, DefaultClientConfig())
}

func ConnectContext(
	ctx context.Context,
	address tracker.PeerAddress,
	infoHash [20]byte,
	peerID [20]byte,
	config ClientConfig,
) (*Client, error) {
	config = withClientDefaults(config)
	dialer := net.Dialer{Timeout: config.DialTimeout}
	hostPort := net.JoinHostPort(address.IP.String(), strconv.Itoa(int(address.Port)))
	connection, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, err
	}
	client := NewClient(connection, address, config)
	if err := client.performHandshake(infoHash, peerID); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// NewClient wraps an established connection. It does not perform a handshake;
// this is useful for accepted connections and protocol-level tests.
func NewClient(connection net.Conn, address tracker.PeerAddress, config ClientConfig) *Client {
	return &Client{Conn: connection, Peer: address, config: withClientDefaults(config)}
}

func withClientDefaults(config ClientConfig) ClientConfig {
	defaults := DefaultClientConfig()
	if config.DialTimeout <= 0 {
		config.DialTimeout = defaults.DialTimeout
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = defaults.HandshakeTimeout
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = defaults.ReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaults.WriteTimeout
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = defaults.MaxMessageSize
	}
	return config
}

func (c *Client) performHandshake(infoHash [20]byte, peerID [20]byte) error {
	if err := c.Conn.SetDeadline(time.Now().Add(c.config.HandshakeTimeout)); err != nil {
		return err
	}
	defer func() {
		_ = c.Conn.SetDeadline(time.Time{}) // The connection may already be unusable.
	}()

	request := (&Handshake{InfoHash: infoHash, PeerID: peerID}).Serialize()
	if err := writeFull(c.Conn, request); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}

	var protocolLength [1]byte
	if _, err := io.ReadFull(c.Conn, protocolLength[:]); err != nil {
		return fmt.Errorf("read handshake length: %w", err)
	}
	if protocolLength[0] == 0 || protocolLength[0] > 64 {
		return errors.New("invalid handshake protocol length")
	}
	responseBytes := make([]byte, 49+int(protocolLength[0]))
	responseBytes[0] = protocolLength[0]
	if _, err := io.ReadFull(c.Conn, responseBytes[1:]); err != nil {
		return fmt.Errorf("read handshake: %w", err)
	}
	response, err := ParseHandshake(responseBytes)
	if err != nil {
		return err
	}
	return response.Validate(infoHash)
}

func (c *Client) WriteMessage(message *Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
		return err
	}
	return writeFull(c.Conn, message.Serialize())
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// ReadLoop reads until the context is cancelled or the connection fails. Its
// context-aware sends ensure the goroutine can always terminate when its worker
// exits.
func (c *Client) ReadLoop(ctx context.Context) (<-chan *Message, <-chan error) {
	// An unbuffered message channel preserves wire ordering relative to a
	// subsequent read error: the consumer handles each message before the next
	// read can observe EOF.
	messages := make(chan *Message)
	errorsChannel := make(chan error, 1)
	go func() {
		defer close(messages)
		defer close(errorsChannel)
		stopCancellationWatch := make(chan struct{})
		defer close(stopCancellationWatch)
		go func() {
			select {
			case <-ctx.Done():
				// Interrupt an in-flight read without taking ownership of closing
				// the connection; RunClient performs the close.
				_ = c.Conn.SetReadDeadline(time.Now())
			case <-stopCancellationWatch:
			}
		}()
		for {
			if err := c.Conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout)); err != nil {
				sendError(ctx, errorsChannel, err)
				return
			}
			message, err := ReadMessageWithLimit(c.Conn, c.config.MaxMessageSize)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				sendError(ctx, errorsChannel, err)
				return
			}
			select {
			case messages <- message:
			case <-ctx.Done():
				return
			}
		}
	}()
	return messages, errorsChannel
}

func sendError(ctx context.Context, target chan<- error, err error) {
	select {
	case target <- err:
	case <-ctx.Done():
	}
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}
