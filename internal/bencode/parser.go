// Package bencode parses the bencoding format used by BitTorrent metainfo and
// tracker responses.
package bencode

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"math"
	"strconv"
)

const (
	// DefaultMaxStringLength is deliberately generous enough for large pieces
	// fields while still preventing an input from requesting an unbounded
	// allocation.
	DefaultMaxStringLength = 64 << 20
	DefaultMaxDepth        = 512
)

type BValue struct {
	Value any
	Start int
	End   int
}

type BString []byte
type BInt int64
type BList []BValue
type BDict map[string]BValue

type Parser struct {
	data            []byte
	index           int
	maxStringLength int
	maxDepth        int
}

func NewParser(data []byte) *Parser {
	return &Parser{
		data:            data,
		maxStringLength: DefaultMaxStringLength,
		maxDepth:        DefaultMaxDepth,
	}
}

// SetLimits overrides parser resource limits. Non-positive values retain the
// current setting.
func (p *Parser) SetLimits(maxStringLength, maxDepth int) {
	if maxStringLength > 0 {
		p.maxStringLength = maxStringLength
	}
	if maxDepth > 0 {
		p.maxDepth = maxDepth
	}
}

// Parse parses exactly one value and rejects trailing input.
func (p *Parser) Parse() (*BValue, error) {
	value, err := p.parseValue(0)
	if err != nil {
		return nil, err
	}
	if p.index != len(p.data) {
		return nil, fmt.Errorf("trailing data at byte %d", p.index)
	}
	return value, nil
}

func (p *Parser) parseValue(depth int) (*BValue, error) {
	if depth > p.maxDepth {
		return nil, fmt.Errorf("maximum nesting depth %d exceeded", p.maxDepth)
	}
	if p.index >= len(p.data) {
		return nil, errors.New("unexpected end of input")
	}

	start := p.index
	var (
		value any
		err   error
	)

	switch current := p.data[p.index]; {
	case current == 'i':
		value, err = p.parseInteger()
	case current == 'l':
		value, err = p.parseList(depth + 1)
	case current == 'd':
		value, err = p.parseDictionary(depth + 1)
	case current >= '0' && current <= '9':
		value, err = p.parseString()
	default:
		return nil, fmt.Errorf("invalid bencode token 0x%02x at byte %d", current, p.index)
	}
	if err != nil {
		return nil, err
	}

	return &BValue{Value: value, Start: start, End: p.index}, nil
}

func (p *Parser) parseInteger() (BInt, error) {
	p.index++ // i
	start := p.index
	for p.index < len(p.data) && p.data[p.index] != 'e' {
		p.index++
	}
	if p.index >= len(p.data) {
		return 0, errors.New("unterminated integer")
	}

	number := p.data[start:p.index]
	p.index++ // e
	if len(number) == 0 {
		return 0, errors.New("empty integer")
	}
	if number[0] == '-' {
		if len(number) == 1 {
			return 0, errors.New("integer contains only a sign")
		}
		if number[1] == '0' {
			return 0, errors.New("negative zero or leading zero is not canonical")
		}
		number = number[1:]
	} else if len(number) > 1 && number[0] == '0' {
		return 0, errors.New("integer with a leading zero is not canonical")
	}
	for _, digit := range number {
		if digit < '0' || digit > '9' {
			return 0, errors.New("integer contains a non-digit")
		}
	}

	value, err := strconv.ParseInt(string(p.data[start:p.index-1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer: %w", err)
	}
	return BInt(value), nil
}

func (p *Parser) parseString() (BString, error) {
	start := p.index
	for p.index < len(p.data) && p.data[p.index] != ':' {
		if p.data[p.index] < '0' || p.data[p.index] > '9' {
			return nil, fmt.Errorf("invalid string length at byte %d", p.index)
		}
		p.index++
	}
	if p.index >= len(p.data) {
		return nil, errors.New("unterminated string length")
	}
	if p.index == start {
		return nil, errors.New("empty string length")
	}
	if p.index-start > 1 && p.data[start] == '0' {
		return nil, errors.New("string length with a leading zero is not canonical")
	}

	length64, err := strconv.ParseInt(string(p.data[start:p.index]), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid string length: %w", err)
	}
	p.index++ // :
	if length64 > int64(p.maxStringLength) {
		return nil, fmt.Errorf("string length %d exceeds limit %d", length64, p.maxStringLength)
	}
	if length64 > int64(len(p.data)-p.index) {
		return nil, errors.New("string exceeds input length")
	}

	length := int(length64)
	end := p.index + length
	value := make([]byte, length)
	copy(value, p.data[p.index:end])
	p.index = end
	return BString(value), nil
}

func (p *Parser) parseList(depth int) (BList, error) {
	p.index++ // l
	result := make(BList, 0)
	for {
		if p.index >= len(p.data) {
			return nil, errors.New("unterminated list")
		}
		if p.data[p.index] == 'e' {
			p.index++
			return result, nil
		}
		value, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		result = append(result, *value)
	}
}

func (p *Parser) parseDictionary(depth int) (BDict, error) {
	p.index++ // d
	result := make(BDict)
	var previous []byte
	for {
		if p.index >= len(p.data) {
			return nil, errors.New("unterminated dictionary")
		}
		if p.data[p.index] == 'e' {
			p.index++
			return result, nil
		}
		if p.data[p.index] < '0' || p.data[p.index] > '9' {
			return nil, fmt.Errorf("dictionary key is not a string at byte %d", p.index)
		}
		key, err := p.parseString()
		if err != nil {
			return nil, err
		}
		if previous != nil && string(previous) >= string(key) {
			return nil, errors.New("dictionary keys are not strictly sorted")
		}
		previous = append(previous[:0], key...)

		value, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		result[string(key)] = *value
	}
}

// ExtractInfoHash returns the SHA-1 hash of the exact encoded info dictionary.
func ExtractInfoHash(root *BValue, rawData []byte) ([20]byte, error) {
	if root == nil {
		return [20]byte{}, errors.New("root value is nil")
	}
	dict, ok := root.Value.(BDict)
	if !ok {
		return [20]byte{}, errors.New("root value is not a dictionary")
	}
	infoValue, ok := dict["info"]
	if !ok {
		return [20]byte{}, errors.New(`missing "info" dictionary`)
	}
	if infoValue.Start < 0 || infoValue.End < infoValue.Start || infoValue.End > len(rawData) {
		return [20]byte{}, errors.New("info dictionary range is outside raw input")
	}
	return sha1.Sum(rawData[infoValue.Start:infoValue.End]), nil
}

func AsString(v BValue) (string, error) {
	value, ok := v.Value.(BString)
	if !ok {
		return "", errors.New("value is not a string")
	}
	return string(value), nil
}

func AsBytes(v BValue) ([]byte, error) {
	value, ok := v.Value.(BString)
	if !ok {
		return nil, errors.New("value is not bytes")
	}
	return value, nil
}

func AsInt64(v BValue) (int64, error) {
	value, ok := v.Value.(BInt)
	if !ok {
		return 0, errors.New("value is not an integer")
	}
	return int64(value), nil
}

func AsInt(v BValue) (int, error) {
	value, err := AsInt64(v)
	if err != nil {
		return 0, err
	}
	if strconv.IntSize == 32 && (value < math.MinInt32 || value > math.MaxInt32) {
		return 0, errors.New("integer does not fit in int")
	}
	return int(value), nil
}

func AsDict(v BValue) (BDict, error) {
	value, ok := v.Value.(BDict)
	if !ok {
		return nil, errors.New("value is not a dictionary")
	}
	return value, nil
}

func AsList(v BValue) (BList, error) {
	value, ok := v.Value.(BList)
	if !ok {
		return nil, errors.New("value is not a list")
	}
	return value, nil
}
