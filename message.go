package ipc

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

type MessageType int

const (
	Ack MessageType = iota
	Connect
	Disconnect
	Data
)

type Message struct {
	ID     string
	Type   MessageType
	Data   []byte
	Source string
}

func encodeMessage(message *Message) ([]byte, error) {
	buf := bytes.NewBuffer([]byte{})
	enc := gob.NewEncoder(buf)
	err := enc.Encode(message)
	if err != nil {
		return nil, fmt.Errorf("failed to encode message: %v", err)
	}
	return buf.Bytes(), nil
}

func decodeMessage(data []byte, message *Message) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	err := dec.Decode(message)
	if err != nil {
		return fmt.Errorf("failed to decode message: %v", err)
	}
	return nil
}
