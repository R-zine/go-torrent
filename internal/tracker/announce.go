package tracker

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"bittorrent-client/internal/bencode"
	"bittorrent-client/internal/torrent"
)

type PeerAddress struct {
	IP   net.IP
	Port uint16
}

type AnnounceResponse struct {
	Interval int
	Peers    []PeerAddress
}

func Announce(
	meta *torrent.TorrentMeta,
	peerID [20]byte,
	port uint16,
) (*AnnounceResponse, error) {

	baseURL, err := url.Parse(meta.Announce)
	if err != nil {
		return nil, err
	}

	params := url.Values{}

	params.Set("peer_id", string(peerID[:]))
	params.Set("port", strconv.Itoa(int(port)))
	params.Set("uploaded", "0")
	params.Set("downloaded", "0")
	params.Set("left", strconv.Itoa(meta.Length))
	params.Set("compact", "1")

	// IMPORTANT:
	// info_hash must be inserted manually as raw bytes
	// and NOT hex encoded.
	params.Set("info_hash", string(meta.InfoHash[:]))

	baseURL.RawQuery = params.Encode()

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Get(baseURL.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
if err != nil {
	return nil, err
}


	parser := bencode.NewParser(data)

	root, err := parser.Parse()
	if err != nil {
		return nil, err
	}

	rootDict, err := bencode.AsDict(*root)
	if err != nil {
		return nil, err
	}

	intervalVal, ok := rootDict["interval"]
	if !ok {
		return nil, fmt.Errorf("tracker response missing interval")
	}

	interval, err := bencode.AsInt(intervalVal)
	if err != nil {
		return nil, err
	}

	peersVal, ok := rootDict["peers"]
	if !ok {
		return nil, fmt.Errorf("tracker response missing peers")
	}

	compactPeers, err := bencode.AsBytes(peersVal)
	if err != nil {
		return nil, err
	}

	peers, err := parseCompactPeers(compactPeers)
	if err != nil {
		return nil, err
	}

	return &AnnounceResponse{
		Interval: interval,
		Peers:    peers,
	}, nil
}

func parseCompactPeers(data []byte) ([]PeerAddress, error) {
	// Compact peer format:
	// 4 bytes IP + 2 bytes port

	if len(data)%6 != 0 {
		return nil, fmt.Errorf("invalid compact peer list")
	}

	var peers []PeerAddress

	for i := 0; i < len(data); i += 6 {
		ip := net.IP(data[i : i+4])

		port := binary.BigEndian.Uint16(data[i+4 : i+6])

		peers = append(peers, PeerAddress{
			IP:   ip,
			Port: port,
		})
	}

	return peers, nil
}