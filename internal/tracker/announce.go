// Package tracker implements HTTP(S) and UDP BitTorrent tracker announces.
package tracker

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bittorrent-client/internal/bencode"
	"bittorrent-client/internal/torrent"
)

const (
	defaultResponseLimit int64 = 4 << 20
	udpProtocolID              = 0x41727101980
	maxIntervalSeconds         = 7 * 24 * 60 * 60
)

type PeerAddress struct {
	IP   net.IP
	Port uint16
}

func (p PeerAddress) String() string {
	return net.JoinHostPort(p.IP.String(), strconv.Itoa(int(p.Port)))
}

type AnnounceResponse struct {
	Interval int
	Peers    []PeerAddress
	Seeders  int
	Leechers int
}

type AnnounceOptions struct {
	Port       uint16
	Uploaded   int64
	Downloaded int64
	Left       int64
	Event      string
	NumWant    int32
}

type Client struct {
	HTTPClient    *http.Client
	Timeout       time.Duration
	ResponseLimit int64
}

func NewClient() *Client {
	return &Client{
		HTTPClient:    &http.Client{Timeout: 15 * time.Second},
		Timeout:       15 * time.Second,
		ResponseLimit: defaultResponseLimit,
	}
}

func Announce(meta *torrent.TorrentMeta, peerID [20]byte, port uint16) (*AnnounceResponse, error) {
	if meta == nil {
		return nil, errors.New("torrent metadata is nil")
	}
	return NewClient().AnnounceFirst(context.Background(), meta, peerID, AnnounceOptions{
		Port:    port,
		Left:    meta.Length,
		Event:   "started",
		NumWant: -1,
	})
}

// AnnounceFirst tries metainfo trackers in order until one responds. This
// provides announce-list failover without coupling the downloader to a scheme.
func (c *Client) AnnounceFirst(
	ctx context.Context,
	meta *torrent.TorrentMeta,
	peerID [20]byte,
	options AnnounceOptions,
) (*AnnounceResponse, error) {
	if meta == nil {
		return nil, errors.New("torrent metadata is nil")
	}
	urls := meta.TrackerURLs()
	if len(urls) == 0 {
		return nil, errors.New("torrent has no tracker URLs")
	}
	var announceErrors []error
	var firstSuccessful *AnnounceResponse
	for _, trackerURL := range urls {
		response, err := c.AnnounceURL(ctx, trackerURL, meta, peerID, options)
		if err == nil && (len(response.Peers) > 0 || options.Event == "completed" || options.Event == "stopped") {
			return response, nil
		}
		if err == nil {
			if firstSuccessful == nil {
				firstSuccessful = response
			}
			continue
		}
		announceErrors = append(announceErrors, fmt.Errorf("%s: %w", trackerURL, err))
	}
	if firstSuccessful != nil {
		return firstSuccessful, nil
	}
	return nil, fmt.Errorf("all trackers failed: %w", errors.Join(announceErrors...))
}

func (c *Client) AnnounceURL(
	ctx context.Context,
	trackerURL string,
	meta *torrent.TorrentMeta,
	peerID [20]byte,
	options AnnounceOptions,
) (*AnnounceResponse, error) {
	if options.Uploaded < 0 || options.Downloaded < 0 || options.Left < 0 {
		return nil, errors.New("announce byte counts must not be negative")
	}
	if options.Event != "" && options.Event != "started" && options.Event != "completed" && options.Event != "stopped" {
		return nil, fmt.Errorf("invalid announce event %q", options.Event)
	}
	parsedURL, err := url.Parse(trackerURL)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(parsedURL.Scheme) {
	case "http", "https":
		return c.announceHTTP(ctx, parsedURL, meta, peerID, options)
	case "udp":
		return c.announceUDP(ctx, parsedURL, meta, peerID, options)
	default:
		return nil, fmt.Errorf("unsupported tracker scheme %q", parsedURL.Scheme)
	}
}

func (c *Client) announceHTTP(
	ctx context.Context,
	baseURL *url.URL,
	meta *torrent.TorrentMeta,
	peerID [20]byte,
	options AnnounceOptions,
) (*AnnounceResponse, error) {
	query := baseURL.Query()
	query.Set("info_hash", string(meta.InfoHash[:]))
	query.Set("peer_id", string(peerID[:]))
	query.Set("port", strconv.Itoa(int(options.Port)))
	query.Set("uploaded", strconv.FormatInt(options.Uploaded, 10))
	query.Set("downloaded", strconv.FormatInt(options.Downloaded, 10))
	query.Set("left", strconv.FormatInt(options.Left, 10))
	query.Set("compact", "1")
	if options.Event != "" {
		query.Set("event", options.Event)
	}
	if options.NumWant != 0 {
		query.Set("numwant", strconv.FormatInt(int64(options.NumWant), 10))
	}
	requestURL := *baseURL
	requestURL.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = NewClient().HTTPClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("tracker returned HTTP %s", response.Status)
	}

	limit := c.ResponseLimit
	if limit <= 0 {
		limit = defaultResponseLimit
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("tracker response exceeds %d bytes", limit)
	}
	return parseAnnounceResponse(data)
}

func parseAnnounceResponse(data []byte) (*AnnounceResponse, error) {
	root, err := bencode.NewParser(data).Parse()
	if err != nil {
		return nil, fmt.Errorf("parse tracker response: %w", err)
	}
	dict, err := bencode.AsDict(*root)
	if err != nil {
		return nil, err
	}
	if failureValue, ok := dict["failure reason"]; ok {
		reason, err := bencode.AsString(failureValue)
		if err != nil {
			return nil, errors.New("tracker returned an invalid failure reason")
		}
		return nil, fmt.Errorf("tracker failure: %s", reason)
	}
	intervalValue, ok := dict["interval"]
	if !ok {
		return nil, errors.New("tracker response missing interval")
	}
	interval, err := bencode.AsInt(intervalValue)
	if err != nil || interval <= 0 || interval > maxIntervalSeconds {
		return nil, errors.New("tracker response has an invalid interval")
	}

	result := &AnnounceResponse{Interval: interval}
	if completeValue, ok := dict["complete"]; ok {
		result.Seeders, err = bencode.AsInt(completeValue)
		if err != nil || result.Seeders < 0 {
			return nil, errors.New("tracker response has an invalid complete count")
		}
	}
	if incompleteValue, ok := dict["incomplete"]; ok {
		result.Leechers, err = bencode.AsInt(incompleteValue)
		if err != nil || result.Leechers < 0 {
			return nil, errors.New("tracker response has an invalid incomplete count")
		}
	}
	if peersValue, ok := dict["peers"]; ok {
		result.Peers, err = parsePeersValue(peersValue)
		if err != nil {
			return nil, err
		}
	}
	if peers6Value, ok := dict["peers6"]; ok {
		compact, err := bencode.AsBytes(peers6Value)
		if err != nil {
			return nil, fmt.Errorf("peers6: %w", err)
		}
		ipv6Peers, err := parseCompactPeers6(compact)
		if err != nil {
			return nil, err
		}
		result.Peers = append(result.Peers, ipv6Peers...)
	}
	result.Peers = uniquePeers(result.Peers)
	return result, nil
}

func parsePeersValue(value bencode.BValue) ([]PeerAddress, error) {
	if compact, err := bencode.AsBytes(value); err == nil {
		return parseCompactPeers(compact)
	}
	list, err := bencode.AsList(value)
	if err != nil {
		return nil, errors.New("tracker peers is neither compact bytes nor a list")
	}
	peers := make([]PeerAddress, 0, len(list))
	for i, item := range list {
		dict, err := bencode.AsDict(item)
		if err != nil {
			return nil, fmt.Errorf("peer %d: %w", i, err)
		}
		ipValue, hasIP := dict["ip"]
		portValue, hasPort := dict["port"]
		if !hasIP || !hasPort {
			return nil, fmt.Errorf("peer %d is missing ip or port", i)
		}
		ipString, err := bencode.AsString(ipValue)
		if err != nil {
			return nil, fmt.Errorf("peer %d ip: %w", i, err)
		}
		ip := net.ParseIP(ipString)
		if ip == nil {
			return nil, fmt.Errorf("peer %d has invalid IP %q", i, ipString)
		}
		port, err := bencode.AsInt(portValue)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("peer %d has invalid port", i)
		}
		peers = append(peers, PeerAddress{IP: ip, Port: uint16(port)})
	}
	return peers, nil
}

func parseCompactPeers(data []byte) ([]PeerAddress, error) {
	if len(data)%6 != 0 {
		return nil, errors.New("invalid compact IPv4 peer list")
	}
	peers := make([]PeerAddress, 0, len(data)/6)
	for i := 0; i < len(data); i += 6 {
		ip := append(net.IP(nil), data[i:i+4]...)
		port := binary.BigEndian.Uint16(data[i+4 : i+6])
		if port != 0 {
			peers = append(peers, PeerAddress{IP: ip, Port: port})
		}
	}
	return peers, nil
}

func parseCompactPeers6(data []byte) ([]PeerAddress, error) {
	if len(data)%18 != 0 {
		return nil, errors.New("invalid compact IPv6 peer list")
	}
	peers := make([]PeerAddress, 0, len(data)/18)
	for i := 0; i < len(data); i += 18 {
		ip := append(net.IP(nil), data[i:i+16]...)
		port := binary.BigEndian.Uint16(data[i+16 : i+18])
		if port != 0 {
			peers = append(peers, PeerAddress{IP: ip, Port: port})
		}
	}
	return peers, nil
}

func uniquePeers(peers []PeerAddress) []PeerAddress {
	seen := make(map[string]struct{})
	result := make([]PeerAddress, 0, len(peers))
	for _, address := range peers {
		key := address.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, address)
	}
	return result
}

func (c *Client) announceUDP(
	ctx context.Context,
	trackerURL *url.URL,
	meta *torrent.TorrentMeta,
	peerID [20]byte,
	options AnnounceOptions,
) (*AnnounceResponse, error) {
	if trackerURL.Host == "" {
		return nil, errors.New("UDP tracker has no host")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	connection, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "udp", trackerURL.Host)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	stopCancellationWatch := make(chan struct{})
	defer close(stopCancellationWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.SetDeadline(time.Now())
		case <-stopCancellationWatch:
		}
	}()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}

	connectTransaction, err := randomUint32()
	if err != nil {
		return nil, err
	}
	connectRequest := make([]byte, 16)
	binary.BigEndian.PutUint64(connectRequest[0:8], udpProtocolID)
	binary.BigEndian.PutUint32(connectRequest[8:12], 0)
	binary.BigEndian.PutUint32(connectRequest[12:16], connectTransaction)
	if err := writeDatagram(connection, connectRequest); err != nil {
		return nil, err
	}
	connectResponse, err := readUDPResponse(connection)
	if err != nil {
		return nil, err
	}
	if err := validateUDPResponse(connectResponse, 0, connectTransaction, 16); err != nil {
		return nil, err
	}
	connectionID := binary.BigEndian.Uint64(connectResponse[8:16])

	announceTransaction, err := randomUint32()
	if err != nil {
		return nil, err
	}
	key, err := randomUint32()
	if err != nil {
		return nil, err
	}
	request := make([]byte, 98)
	binary.BigEndian.PutUint64(request[0:8], connectionID)
	binary.BigEndian.PutUint32(request[8:12], 1)
	binary.BigEndian.PutUint32(request[12:16], announceTransaction)
	copy(request[16:36], meta.InfoHash[:])
	copy(request[36:56], peerID[:])
	binary.BigEndian.PutUint64(request[56:64], uint64(options.Downloaded))
	binary.BigEndian.PutUint64(request[64:72], uint64(options.Left))
	binary.BigEndian.PutUint64(request[72:80], uint64(options.Uploaded))
	binary.BigEndian.PutUint32(request[80:84], udpEvent(options.Event))
	binary.BigEndian.PutUint32(request[88:92], key)
	numWant := options.NumWant
	if numWant == 0 {
		numWant = -1
	}
	binary.BigEndian.PutUint32(request[92:96], uint32(numWant))
	binary.BigEndian.PutUint16(request[96:98], options.Port)
	if err := writeDatagram(connection, request); err != nil {
		return nil, err
	}
	announceResponse, err := readUDPResponse(connection)
	if err != nil {
		return nil, err
	}
	if err := validateUDPResponse(announceResponse, 1, announceTransaction, 20); err != nil {
		return nil, err
	}
	var peers []PeerAddress
	if remote, ok := connection.RemoteAddr().(*net.UDPAddr); ok && remote.IP.To4() == nil {
		peers, err = parseCompactPeers6(announceResponse[20:])
	} else {
		peers, err = parseCompactPeers(announceResponse[20:])
	}
	if err != nil {
		return nil, err
	}
	interval := int(binary.BigEndian.Uint32(announceResponse[8:12]))
	if interval <= 0 || interval > maxIntervalSeconds {
		return nil, errors.New("UDP tracker returned an invalid interval")
	}
	return &AnnounceResponse{
		Interval: interval,
		Leechers: int(binary.BigEndian.Uint32(announceResponse[12:16])),
		Seeders:  int(binary.BigEndian.Uint32(announceResponse[16:20])),
		Peers:    peers,
	}, nil
}

func readUDPResponse(connection net.Conn) ([]byte, error) {
	buffer := make([]byte, 64<<10)
	n, err := connection.Read(buffer)
	if err != nil {
		return nil, err
	}
	return buffer[:n], nil
}

func validateUDPResponse(data []byte, expectedAction, transaction uint32, minimumLength int) error {
	if len(data) < 8 {
		return errors.New("UDP tracker response is too short")
	}
	action := binary.BigEndian.Uint32(data[0:4])
	responseTransaction := binary.BigEndian.Uint32(data[4:8])
	if responseTransaction != transaction {
		return errors.New("UDP tracker transaction ID mismatch")
	}
	if action == 3 {
		return fmt.Errorf("UDP tracker failure: %s", data[8:])
	}
	if action != expectedAction {
		return fmt.Errorf("UDP tracker action %d, expected %d", action, expectedAction)
	}
	if len(data) < minimumLength {
		return errors.New("UDP tracker response is too short")
	}
	return nil
}

func writeDatagram(connection net.Conn, data []byte) error {
	n, err := connection.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func randomUint32() (uint32, error) {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(value[:]), nil
}

func udpEvent(event string) uint32 {
	switch event {
	case "completed":
		return 1
	case "started":
		return 2
	case "stopped":
		return 3
	default:
		return 0
	}
}
