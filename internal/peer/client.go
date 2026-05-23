package peer

import (
	"fmt"
	"io"
	"net"
	"time"

	"bittorrent-client/internal/tracker"
)

type Client struct {
	Conn net.Conn

	Peer tracker.PeerAddress
}

func Connect(
	peer tracker.PeerAddress,
	infoHash [20]byte,
	peerID [20]byte,
) (*Client, error) {

	address := fmt.Sprintf("%s:%d", peer.IP.String(), peer.Port)

	conn, err := net.DialTimeout(
		"tcp",
		address,
		3*time.Second,
	)
	if err != nil {
		return nil, err
	}

	client := &Client{
		Conn: conn,
		Peer: peer,
	}

	err = client.performHandshake(infoHash, peerID)
	if err != nil {
		conn.Close()
		return nil, err
	}

	return client, nil
}

func (c *Client) performHandshake(
	infoHash [20]byte,
	peerID [20]byte,
) error {

	request := Handshake{
		InfoHash: infoHash,
		PeerID:   peerID,
	}

	_, err := c.Conn.Write(request.Serialize())
	if err != nil {
		return err
	}

	responseBuffer := make([]byte, 68)

	_, err = io.ReadFull(c.Conn, responseBuffer)
	if err != nil {
		return err
	}

	response, err := ParseHandshake(responseBuffer)
	if err != nil {
		return err
	}

	err = response.Validate(infoHash)
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) ReadLoop(messages chan *Message, errors chan error) {
	go func() {
		for {
			msg, err := ReadMessage(c.Conn)
			if err != nil {
				errors <- err
				return
			}

			messages <- msg
		}
	}()
}

func (c *Client) Close() error {
	return c.Conn.Close()
}
