package testutil

import (
	"encoding/binary"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// crc32Tab is the CRC-32 (IEEE) table for frame building.
var crc32Tab = crc32.IEEETable

// NewTCP4TestServer creates an httptest.Server bound to tcp4 to avoid IPv6 bind failures in sandboxed environments.
func NewTCP4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on tcp4: %v", err)
	}
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	return srv
}

// BuildFrame constructs a binary event stream frame with a single :event-type header.
func BuildFrame(eventType string, payload []byte) []byte {
	headerName := []byte(":event-type")
	eventTypeBytes := []byte(eventType)
	headers := []byte{byte(len(headerName))}
	headers = append(headers, headerName...)
	headers = append(headers, 7) // string type
	headers = append(headers, byte(len(eventTypeBytes)>>8), byte(len(eventTypeBytes)))
	headers = append(headers, eventTypeBytes...)

	return AssembleFrame(headers, payload)
}

// AssembleFrame builds a complete AWS Event Stream frame with proper CRC32C checksums.
func AssembleFrame(headers, payload []byte) []byte {
	headersLen := uint32(len(headers))
	totalLen := 12 + headersLen + uint32(len(payload)) + 4

	var prelude [8]byte
	binary.BigEndian.PutUint32(prelude[0:4], totalLen)
	binary.BigEndian.PutUint32(prelude[4:8], headersLen)
	preludeCRC := crc32.Checksum(prelude[:], crc32Tab)

	frame := make([]byte, 0, totalLen)
	frame = append(frame, prelude[:]...)
	frame = binary.BigEndian.AppendUint32(frame, preludeCRC)
	frame = append(frame, headers...)
	frame = append(frame, payload...)

	msgCRC := crc32.Checksum(frame, crc32Tab)
	frame = binary.BigEndian.AppendUint32(frame, msgCRC)
	return frame
}

// BuildFrameWithExtraHeaders builds a frame with additional headers before :event-type.
func BuildFrameWithExtraHeaders(extraHeaders []byte, eventType string, payload []byte) []byte {
	headerName := []byte(":event-type")
	eventTypeBytes := []byte(eventType)
	etHeader := []byte{byte(len(headerName))}
	etHeader = append(etHeader, headerName...)
	etHeader = append(etHeader, 7)
	etHeader = append(etHeader, byte(len(eventTypeBytes)>>8), byte(len(eventTypeBytes)))
	etHeader = append(etHeader, eventTypeBytes...)

	headers := append(extraHeaders, etHeader...)
	return AssembleFrame(headers, payload)
}
