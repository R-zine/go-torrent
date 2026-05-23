package torrent

import (
	"errors"
	"fmt"

	"bittorrent-client/internal/bencode"
)

type TorrentMeta struct {
	Announce    string
	Name        string
	Length      int
	PieceLength int
	Pieces      [][]byte
	InfoHash    [20]byte
}


func ExtractMeta(root *bencode.BValue, rawData []byte) (*TorrentMeta, error) {
	rootDict, err := bencode.AsDict(*root)
	if err != nil {
		return nil, err
	}

	announceVal, ok := rootDict["announce"]
	if !ok {
		return nil, errors.New("missing announce")
	}

	announce, err := bencode.AsString(announceVal)
	if err != nil {
		return nil, err
	}

	infoVal, ok := rootDict["info"]
	if !ok {
		return nil, errors.New("missing info dictionary")
	}

	infoDict, err := bencode.AsDict(infoVal)
	if err != nil {
		return nil, err
	}

	nameVal, ok := infoDict["name"]
	if !ok {
		return nil, errors.New("missing name")
	}

	name, err := bencode.AsString(nameVal)
	if err != nil {
		return nil, err
	}

	lengthVal, ok := infoDict["length"]
	if !ok {
		return nil, errors.New("missing length")
	}

	length, err := bencode.AsInt(lengthVal)
	if err != nil {
		return nil, err
	}

	pieceLengthVal, ok := infoDict["piece length"]
	if !ok {
		return nil, errors.New("missing piece length")
	}

	pieceLength, err := bencode.AsInt(pieceLengthVal)
	if err != nil {
		return nil, err
	}

	piecesVal, ok := infoDict["pieces"]
	if !ok {
		return nil, errors.New("missing pieces")
	}

	rawPieces, err := bencode.AsBytes(piecesVal)
	if err != nil {
		return nil, err
	}

	if len(rawPieces)%20 != 0 {
		return nil, fmt.Errorf("invalid pieces length")
	}

	var pieces [][]byte

	for i := 0; i < len(rawPieces); i += 20 {
		hash := make([]byte, 20)
		copy(hash, rawPieces[i:i+20])
		pieces = append(pieces, hash)
	}

	infoHash, err := bencode.ExtractInfoHash(root, rawData)
	if err != nil {
		return nil, err
	}

	return &TorrentMeta{
		Announce:    announce,
		Name:        name,
		Length:      length,
		PieceLength: pieceLength,
		Pieces:      pieces,
		InfoHash:    infoHash,
	}, nil
}