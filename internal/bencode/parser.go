package bencode

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"strconv"
	"unicode"
)

type BValue struct {
	Value interface{}
	Start int
	End   int
}

type BString []byte
type BInt int
type BList []BValue
type BDict map[string]BValue

type Parser struct {
	data  []byte
	index int
}

func NewParser(data []byte) *Parser {
	return &Parser{
		data: data,
	}
}

func (p *Parser) Parse() (*BValue, error) {
	if p.index >= len(p.data) {
		return nil, errors.New("unexpected end of input")
	}

	start := p.index
	current := p.data[p.index]

	var value interface{}
	var err error

	switch {
	case current == 'i':
		value, err = p.parseInteger()

	case current == 'l':
		value, err = p.parseList()

	case current == 'd':
		value, err = p.parseDictionary()

	case unicode.IsDigit(rune(current)):
		value, err = p.parseString()

	default:
		return nil, fmt.Errorf("invalid bencode token: %c", current)
	}

	if err != nil {
		return nil, err
	}

	return &BValue{
		Value: value,
		Start: start,
		End:   p.index,
	}, nil
}

func (p *Parser) parseInteger() (BInt, error) {
	// skip 'i'
	p.index++

	start := p.index

	for {
		if p.index >= len(p.data) {
			return 0, errors.New("unterminated integer")
		}

		if p.data[p.index] == 'e' {
			break
		}

		p.index++
	}

	numberStr := string(p.data[start:p.index])

	// skip 'e'
	p.index++

	value, err := strconv.Atoi(numberStr)
	if err != nil {
		return 0, err
	}

	return BInt(value), nil
}

func (p *Parser) parseString() (BString, error) {
	start := p.index

	for {
		if p.index >= len(p.data) {
			return nil, errors.New("unterminated string length")
		}

		if p.data[p.index] == ':' {
			break
		}

		p.index++
	}

	lengthStr := string(p.data[start:p.index])

	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		return nil, err
	}

	// skip ':'
	p.index++

	end := p.index + length

	if end > len(p.data) {
		return nil, errors.New("string exceeds input length")
	}

	value := make([]byte, length)
	copy(value, p.data[p.index:end])

	p.index = end

	return BString(value), nil
}

func (p *Parser) parseList() (BList, error) {
	// skip 'l'
	p.index++

	var result BList

	for {
		if p.index >= len(p.data) {
			return nil, errors.New("unterminated list")
		}

		if p.data[p.index] == 'e' {
			break
		}

		value, err := p.Parse()
		if err != nil {
			return nil, err
		}

		result = append(result, *value)
	}

	// skip 'e'
	p.index++

	return result, nil
}

func (p *Parser) parseDictionary() (BDict, error) {
	// skip 'd'
	p.index++

	result := make(BDict)

	for {
		if p.index >= len(p.data) {
			return nil, errors.New("unterminated dictionary")
		}

		if p.data[p.index] == 'e' {
			break
		}

		key, err := p.parseString()
		if err != nil {
			return nil, err
		}

		value, err := p.Parse()
		if err != nil {
			return nil, err
		}

		result[string(key)] = *value
	}

	// skip 'e'
	p.index++

	return result, nil
}

// ExtractInfoHash returns the SHA1 hash of the raw bencoded "info" dictionary.
func ExtractInfoHash(root *BValue, rawData []byte) ([20]byte, error) {
	dict, ok := root.Value.(BDict)
	if !ok {
		return [20]byte{}, errors.New("root value is not a dictionary")
	}

	infoValue, ok := dict["info"]
	if !ok {
		return [20]byte{}, errors.New(`missing "info" dictionary`)
	}

	infoBytes := rawData[infoValue.Start:infoValue.End]

	return sha1.Sum(infoBytes), nil
}

// Helper functions for safer type assertions.

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

func AsInt(v BValue) (int, error) {
	value, ok := v.Value.(BInt)
	if !ok {
		return 0, errors.New("value is not an integer")
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