package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	MsgChoke         = 0
	MsgUnchoke       = 1
	MsgInterested    = 2
	MsgNotInterested = 3
	MsgHave          = 4
	MsgBitfield      = 5
	MsgRequest       = 6
	MsgPiece         = 7
)

type Message struct {
	ID      *byte
	Payload []byte
}

func ReadMessage(r io.Reader) (*Message, error) {
	lengthBuf := make([]byte, 4)

	_, err := io.ReadFull(r, lengthBuf)
	if err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)

	// keep-alive
	if length == 0 {
		return &Message{ID: nil}, nil
	}

	msgBuf := make([]byte, length)

	_, err = io.ReadFull(r, msgBuf)
	if err != nil {
		return nil, err
	}

	id := msgBuf[0]
	payload := msgBuf[1:]

	return &Message{
		ID:      &id,
		Payload: payload,
	}, nil
}

func (m *Message) Serialize() []byte {
	if m.ID == nil {
		return []byte{0, 0, 0, 0}
	}

	length := uint32(len(m.Payload) + 1)

	buf := make([]byte, 4+length)

	binary.BigEndian.PutUint32(buf[0:4], length)

	buf[4] = *m.ID

	copy(buf[5:], m.Payload)

	return buf
}

func NewInterested() *Message {
	id := byte(MsgInterested)
	return &Message{ID: &id}
}

func NewRequest(index, begin, length int) *Message {
	id := byte(MsgRequest)

	payload := make([]byte, 12)

	binary.BigEndian.PutUint32(payload[0:4], uint32(index))
	binary.BigEndian.PutUint32(payload[4:8], uint32(begin))
	binary.BigEndian.PutUint32(payload[8:12], uint32(length))

	return &Message{
		ID:      &id,
		Payload: payload,
	}
}

func ParsePiece(payload []byte) (index, begin int, block []byte, err error) {
	if len(payload) < 8 {
		return 0, 0, nil, fmt.Errorf("invalid piece payload")
	}

	index = int(binary.BigEndian.Uint32(payload[0:4]))
	begin = int(binary.BigEndian.Uint32(payload[4:8]))
	block = payload[8:]

	return
}