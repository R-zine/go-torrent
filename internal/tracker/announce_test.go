package tracker

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"bittorrent-client/internal/torrent"
)

func TestHTTPAnnouncePreservesQueryAndParsesCompactPeers(t *testing.T) {
	infoHash := [20]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 255}
	peerID := [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	compact := []byte{127, 0, 0, 1, 0x1a, 0xe1}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("passkey") != "secret" {
			t.Errorf("passkey = %q", query.Get("passkey"))
		}
		if query.Get("info_hash") != string(infoHash[:]) || query.Get("peer_id") != string(peerID[:]) {
			t.Error("binary identifier query values did not round trip")
		}
		if query.Get("event") != "started" || query.Get("compact") != "1" || query.Get("numwant") != "-1" {
			t.Errorf("query = %v", query)
		}
		writer.Write(encodeTrackerValue(map[string]any{"interval": int64(60), "peers": compact}))
	}))
	defer server.Close()

	meta := &torrent.TorrentMeta{Announce: server.URL + "/announce?passkey=secret", Length: 10, InfoHash: infoHash}
	response, err := NewClient().AnnounceFirst(context.Background(), meta, peerID, AnnounceOptions{
		Port: 6881, Left: 10, Event: "started", NumWant: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Interval != 60 || len(response.Peers) != 1 || response.Peers[0].String() != "127.0.0.1:6881" {
		t.Fatalf("response = %+v", response)
	}
}

func TestHTTPAnnounceParsesDictionaryAndIPv6Peers(t *testing.T) {
	ipv6 := net.ParseIP("2001:db8::1").To16()
	compact6 := append(append([]byte(nil), ipv6...), 0x01, 0xbb)
	body := encodeTrackerValue(map[string]any{
		"complete":   int64(2),
		"incomplete": int64(3),
		"interval":   int64(30),
		"peers": []any{
			map[string]any{"ip": "192.0.2.1", "port": int64(80)},
		},
		"peers6": compact6,
	})
	response, err := parseAnnounceResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Seeders != 2 || response.Leechers != 3 || len(response.Peers) != 2 {
		t.Fatalf("response = %+v", response)
	}
	if response.Peers[1].String() != "[2001:db8::1]:443" {
		t.Fatalf("IPv6 peer = %s", response.Peers[1])
	}
}

func TestHTTPAnnounceErrorsAreDescriptive(t *testing.T) {
	t.Run("failure reason", func(t *testing.T) {
		body := encodeTrackerValue(map[string]any{"failure reason": "not authorized"})
		_, err := parseAnnounceResponse(body)
		if err == nil || !strings.Contains(err.Error(), "not authorized") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("absurd interval", func(t *testing.T) {
		body := encodeTrackerValue(map[string]any{"interval": int64(maxIntervalSeconds + 1), "peers": []byte{}})
		if _, err := parseAnnounceResponse(body); err == nil {
			t.Fatal("absurd tracker interval unexpectedly accepted")
		}
	})

	t.Run("HTTP status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "no", http.StatusForbidden)
		}))
		defer server.Close()
		client := NewClient()
		_, err := client.AnnounceURL(context.Background(), server.URL, &torrent.TorrentMeta{}, [20]byte{}, AnnounceOptions{})
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("response limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Write(bytes.Repeat([]byte{'x'}, 33))
		}))
		defer server.Close()
		client := NewClient()
		client.ResponseLimit = 32
		_, err := client.AnnounceURL(context.Background(), server.URL, &torrent.TorrentMeta{}, [20]byte{}, AnnounceOptions{})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestAnnounceFirstFallsBackToNextTracker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(encodeTrackerValue(map[string]any{"interval": int64(10), "peers": []byte{}}))
	}))
	defer server.Close()
	meta := &torrent.TorrentMeta{
		Announce:     "ftp://unsupported/announce",
		AnnounceList: [][]string{{server.URL}},
	}
	response, err := NewClient().AnnounceFirst(context.Background(), meta, [20]byte{}, AnnounceOptions{})
	if err != nil || response.Interval != 10 {
		t.Fatalf("AnnounceFirst() = %+v, %v", response, err)
	}
}

func TestAnnounceFirstContinuesAfterEmptySuccessfulTracker(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(encodeTrackerValue(map[string]any{"interval": int64(10), "peers": []byte{}}))
	}))
	defer empty.Close()
	withPeer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write(encodeTrackerValue(map[string]any{
			"interval": int64(10),
			"peers":    []byte{127, 0, 0, 1, 0, 80},
		}))
	}))
	defer withPeer.Close()
	meta := &torrent.TorrentMeta{Announce: empty.URL, AnnounceList: [][]string{{withPeer.URL}}}
	response, err := NewClient().AnnounceFirst(context.Background(), meta, [20]byte{}, AnnounceOptions{})
	if err != nil || len(response.Peers) != 1 {
		t.Fatalf("AnnounceFirst() = %+v, %v", response, err)
	}
}

func TestCompactPeerValidationAndDeduplication(t *testing.T) {
	if _, err := parseCompactPeers(make([]byte, 5)); err == nil {
		t.Fatal("invalid IPv4 peers unexpectedly accepted")
	}
	if _, err := parseCompactPeers6(make([]byte, 17)); err == nil {
		t.Fatal("invalid IPv6 peers unexpectedly accepted")
	}
	peers := []PeerAddress{
		{IP: net.ParseIP("127.0.0.1"), Port: 1},
		{IP: net.ParseIP("127.0.0.1"), Port: 1},
	}
	if got := uniquePeers(peers); len(got) != 1 {
		t.Fatalf("uniquePeers() length = %d", len(got))
	}
}

func TestUDPAnnounce(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	serverErrors := make(chan error, 1)
	infoHash := [20]byte{1, 2, 3}
	peerID := [20]byte{4, 5, 6}
	go serveUDPTracker(server, infoHash, peerID, serverErrors)

	client := NewClient()
	client.Timeout = time.Second
	meta := &torrent.TorrentMeta{Announce: "udp://" + server.LocalAddr().String() + "/announce", Length: 123, InfoHash: infoHash}
	response, err := client.AnnounceFirst(context.Background(), meta, peerID, AnnounceOptions{
		Port: 6881, Left: 123, Event: "started", NumWant: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErrors; err != nil {
		t.Fatal(err)
	}
	if response.Interval != 45 || response.Seeders != 8 || response.Leechers != 7 || len(response.Peers) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if response.Peers[0].String() != "203.0.113.5:51413" {
		t.Fatalf("peer = %s", response.Peers[0])
	}
}

func serveUDPTracker(server net.PacketConn, infoHash, peerID [20]byte, result chan<- error) {
	fail := func(format string, args ...any) {
		result <- fmt.Errorf(format, args...)
	}
	buffer := make([]byte, 2048)
	_ = server.SetDeadline(time.Now().Add(time.Second))
	n, clientAddress, err := server.ReadFrom(buffer)
	if err != nil {
		fail("read connect: %v", err)
		return
	}
	if n != 16 || binary.BigEndian.Uint64(buffer[:8]) != udpProtocolID || binary.BigEndian.Uint32(buffer[8:12]) != 0 {
		fail("invalid connect request")
		return
	}
	transaction := binary.BigEndian.Uint32(buffer[12:16])
	connectResponse := make([]byte, 16)
	binary.BigEndian.PutUint32(connectResponse[0:4], 0)
	binary.BigEndian.PutUint32(connectResponse[4:8], transaction)
	binary.BigEndian.PutUint64(connectResponse[8:16], 99)
	if _, err := server.WriteTo(connectResponse, clientAddress); err != nil {
		fail("write connect: %v", err)
		return
	}

	n, clientAddress, err = server.ReadFrom(buffer)
	if err != nil {
		fail("read announce: %v", err)
		return
	}
	if n != 98 || binary.BigEndian.Uint64(buffer[:8]) != 99 || binary.BigEndian.Uint32(buffer[8:12]) != 1 {
		fail("invalid announce request")
		return
	}
	if !bytes.Equal(buffer[16:36], infoHash[:]) || !bytes.Equal(buffer[36:56], peerID[:]) {
		fail("announce identifiers did not match")
		return
	}
	if binary.BigEndian.Uint32(buffer[80:84]) != 2 || binary.BigEndian.Uint16(buffer[96:98]) != 6881 {
		fail("announce event or port did not match")
		return
	}
	transaction = binary.BigEndian.Uint32(buffer[12:16])
	announceResponse := make([]byte, 26)
	binary.BigEndian.PutUint32(announceResponse[0:4], 1)
	binary.BigEndian.PutUint32(announceResponse[4:8], transaction)
	binary.BigEndian.PutUint32(announceResponse[8:12], 45)
	binary.BigEndian.PutUint32(announceResponse[12:16], 7)
	binary.BigEndian.PutUint32(announceResponse[16:20], 8)
	copy(announceResponse[20:24], net.ParseIP("203.0.113.5").To4())
	binary.BigEndian.PutUint16(announceResponse[24:26], 51413)
	if _, err := server.WriteTo(announceResponse, clientAddress); err != nil {
		fail("write announce: %v", err)
		return
	}
	result <- nil
}

func TestUDPResponseValidation(t *testing.T) {
	response := make([]byte, 8)
	binary.BigEndian.PutUint32(response[0:4], 3)
	binary.BigEndian.PutUint32(response[4:8], 4)
	response = append(response, "denied"...)
	if err := validateUDPResponse(response, 1, 4, 20); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v", err)
	}
	if err := validateUDPResponse(make([]byte, 4), 1, 1, 20); err == nil {
		t.Fatal("short response unexpectedly accepted")
	}
}

func TestPeerAddressStringUsesIPv6Brackets(t *testing.T) {
	address := PeerAddress{IP: net.ParseIP("2001:db8::2"), Port: 80}
	if got := address.String(); got != "[2001:db8::2]:80" {
		t.Fatalf("String() = %q", got)
	}
	if _, err := url.Parse("udp://" + address.String()); err != nil {
		t.Fatal(err)
	}
}

func TestAnnounceRejectsInvalidInputs(t *testing.T) {
	if _, err := Announce(nil, [20]byte{}, 1); err == nil {
		t.Fatal("nil metadata unexpectedly accepted")
	}
	client := NewClient()
	meta := &torrent.TorrentMeta{Announce: "http://example.invalid"}
	for _, options := range []AnnounceOptions{
		{Uploaded: -1},
		{Downloaded: -1},
		{Left: -1},
		{Event: "invalid"},
	} {
		if _, err := client.AnnounceURL(context.Background(), meta.Announce, meta, [20]byte{}, options); err == nil {
			t.Errorf("options %+v unexpectedly accepted", options)
		}
	}
}

func encodeTrackerValue(value any) []byte {
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
			panic(fmt.Sprintf("unsupported tracker test type %T", value))
		}
	}
	encode(value)
	return buffer.Bytes()
}
