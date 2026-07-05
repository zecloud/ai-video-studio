package dji

import (
	"encoding/binary"
	"fmt"
)

const (
	pairTransactionID = 0x8092
	pairTarget        = 0x0702
	pairType          = 0x450740
)

var pairPayloadPrefix = []byte{
	0x20, 0x32, 0x38, 0x34, 0x61, 0x65, 0x35, 0x62, 0x38, 0x64, 0x37, 0x36,
	0x62, 0x33, 0x33, 0x37, 0x35, 0x61, 0x30, 0x34, 0x61, 0x36, 0x34, 0x31,
	0x37, 0x61, 0x64, 0x37, 0x31, 0x62, 0x65, 0x61, 0x33,
}

type djiMessage struct {
	Target  uint16
	ID      uint16
	Type    uint32
	Payload []byte
}

func encodePairMessage(pin string) ([]byte, error) {
	if pin == "" {
		return nil, fmt.Errorf("pairing PIN is required")
	}
	payload, err := encodePairPayload(pin)
	if err != nil {
		return nil, err
	}
	return encodeDjiMessage(djiMessage{
		Target:  pairTarget,
		ID:      pairTransactionID,
		Type:    pairType,
		Payload: payload,
	})
}

func encodePairPayload(pin string) ([]byte, error) {
	packedPin, err := packDjiString(pin)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, 0, len(pairPayloadPrefix)+len(packedPin))
	payload = append(payload, pairPayloadPrefix...)
	payload = append(payload, packedPin...)
	return payload, nil
}

func packDjiString(value string) ([]byte, error) {
	if len(value) > 255 {
		return nil, fmt.Errorf("DJI string too long: %d bytes", len(value))
	}
	data := []byte(value)
	packed := make([]byte, 0, 1+len(data))
	packed = append(packed, byte(len(data)))
	packed = append(packed, data...)
	return packed, nil
}

func encodeDjiMessage(message djiMessage) ([]byte, error) {
	if message.Type > 0xffffff {
		return nil, fmt.Errorf("DJI message type 0x%x exceeds 24 bits", message.Type)
	}
	length := 13 + len(message.Payload)
	if length > 255 {
		return nil, fmt.Errorf("DJI message too long: %d bytes", length)
	}

	body := []byte{0x55, byte(length), 0x04}
	body = append(body, djiCRC8(body))
	body = binary.LittleEndian.AppendUint16(body, message.Target)
	body = binary.LittleEndian.AppendUint16(body, message.ID)
	body = append(body, byte(message.Type), byte(message.Type>>8), byte(message.Type>>16))
	body = append(body, message.Payload...)
	crc := djiCRC16(body)
	body = binary.LittleEndian.AppendUint16(body, crc)
	return body, nil
}

func parseDjiMessage(data []byte) (djiMessage, error) {
	if len(data) < 13 {
		return djiMessage{}, fmt.Errorf("DJI message too short: %d bytes", len(data))
	}
	if data[0] != 0x55 {
		return djiMessage{}, fmt.Errorf("bad DJI message prefix 0x%02x", data[0])
	}
	if int(data[1]) != len(data) {
		return djiMessage{}, fmt.Errorf("DJI message length mismatch: header=%d actual=%d", data[1], len(data))
	}
	if data[2] != 0x04 {
		return djiMessage{}, fmt.Errorf("bad DJI message version 0x%02x", data[2])
	}
	if expected := djiCRC8(data[:3]); data[3] != expected {
		return djiMessage{}, fmt.Errorf("bad DJI header CRC: got=0x%02x expected=0x%02x", data[3], expected)
	}
	expectedCRC := djiCRC16(data[:len(data)-2])
	actualCRC := binary.LittleEndian.Uint16(data[len(data)-2:])
	if actualCRC != expectedCRC {
		return djiMessage{}, fmt.Errorf("bad DJI CRC16: got=0x%04x expected=0x%04x", actualCRC, expectedCRC)
	}
	return djiMessage{
		Target:  binary.LittleEndian.Uint16(data[4:6]),
		ID:      binary.LittleEndian.Uint16(data[6:8]),
		Type:    uint32(data[8]) | uint32(data[9])<<8 | uint32(data[10])<<16,
		Payload: append([]byte(nil), data[11:len(data)-2]...),
	}, nil
}

func djiCRC8(data []byte) byte {
	var crc byte = 0xee
	for _, b := range data {
		crc ^= b
		for range 8 {
			if crc&0x01 != 0 {
				crc = (crc >> 1) ^ 0x8c
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func djiCRC16(data []byte) uint16 {
	var crc uint16 = 0x496c
	for _, b := range data {
		crc ^= uint16(b)
		for range 8 {
			if crc&0x0001 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}
