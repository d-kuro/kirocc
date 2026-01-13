package kiroproto

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
)

const maxFrameSize = 4 * 1024 * 1024 // 4MB max frame size

// crc32Tab is the CRC-32 (IEEE) table used by the Kiro event stream.
var crc32Tab = crc32.IEEETable

// readFrame reads one AWS Event Stream binary frame from br.
// Returns (headers, payload, nil) on success, or (nil, nil, io.EOF) on clean end-of-stream.
// Validates both prelude CRC and message CRC.
func readFrame(br *bufio.Reader) (headers, payload []byte, err error) {
	var prelude [12]byte
	n, err := io.ReadFull(br, prelude[:])
	if err != nil {
		// Clean EOF (no bytes read) = normal end-of-stream.
		// Partial read = truncated frame from upstream.
		if n == 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
			return nil, nil, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return nil, nil, fmt.Errorf("truncated prelude: read %d/12 bytes", n)
		}
		return nil, nil, fmt.Errorf("reading prelude: %w", err)
	}

	// Verify prelude CRC (bytes 8-11 are CRC of bytes 0-7).
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])
	if computed := crc32.Checksum(prelude[:8], crc32Tab); computed != preludeCRC {
		// Peek at more bytes to diagnose what the server actually sent.
		extra, _ := br.Peek(br.Buffered())
		if len(extra) > 256 {
			extra = extra[:256]
		}
		slog.Error("prelude CRC mismatch: raw response bytes",
			"prelude_hex", fmt.Sprintf("%x", prelude[:]),
			"next_bytes", fmt.Sprintf("%q", extra),
		)
		return nil, nil, fmt.Errorf("prelude CRC mismatch: got %08x, want %08x", computed, preludeCRC)
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])

	if totalLen < 16 {
		return nil, nil, fmt.Errorf("invalid frame: total_length %d too small", totalLen)
	}
	if totalLen > maxFrameSize {
		return nil, nil, fmt.Errorf("invalid frame: total_length %d exceeds max %d", totalLen, maxFrameSize)
	}
	bodyLen := totalLen - 12
	if headersLen > bodyLen-4 {
		return nil, nil, fmt.Errorf("invalid frame: headers_length %d exceeds body", headersLen)
	}

	remaining := make([]byte, bodyLen)
	if _, err := io.ReadFull(br, remaining); err != nil {
		return nil, nil, fmt.Errorf("reading frame body: %w", err)
	}

	// Verify message CRC (last 4 bytes of frame cover prelude + headers + payload).
	msgCRC := binary.BigEndian.Uint32(remaining[len(remaining)-4:])
	h := crc32.New(crc32Tab)
	h.Write(prelude[:])
	h.Write(remaining[:len(remaining)-4])
	if computed := h.Sum32(); computed != msgCRC {
		return nil, nil, fmt.Errorf("message CRC mismatch: got %08x, want %08x", computed, msgCRC)
	}

	headers = remaining[:headersLen]
	payload = remaining[headersLen : len(remaining)-4]
	return headers, payload, nil
}

// headerValueSizes maps value type IDs to their fixed byte sizes.
// -1 means variable length (2-byte uint16 prefix).
var headerValueSizes = map[byte]int{
	0: 0,  // bool true
	1: 0,  // bool false
	2: 1,  // byte
	3: 2,  // short
	4: 4,  // int
	5: 8,  // long
	6: -1, // byte array (variable)
	7: -1, // string (variable)
	8: 8,  // timestamp
	9: 16, // uuid
}

// extractFrameHeaders walks the headers bytes and returns the values of
// :message-type and :event-type (or :exception-type) headers.
func extractFrameHeaders(headers []byte) (msgType, eventType string) {
	i := 0
	for i < len(headers) {
		nameLen := int(headers[i])
		i++
		if i+nameLen > len(headers) {
			break
		}
		name := string(headers[i : i+nameLen])
		i += nameLen

		if i >= len(headers) {
			break
		}
		valueType := headers[i]
		i++

		size, known := headerValueSizes[valueType]
		if !known {
			break
		}

		var valueLen int
		if size >= 0 {
			valueLen = size
		} else {
			// variable: 2-byte uint16 length prefix
			if i+2 > len(headers) {
				break
			}
			valueLen = int(binary.BigEndian.Uint16(headers[i : i+2]))
			i += 2
		}

		if i+valueLen > len(headers) {
			break
		}
		value := headers[i : i+valueLen]
		i += valueLen

		if valueType == 7 {
			switch name {
			case ":message-type":
				msgType = string(value)
			case ":event-type", ":exception-type":
				eventType = string(value)
			}
		}
	}
	return msgType, eventType
}
