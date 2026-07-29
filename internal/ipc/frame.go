package ipc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

func WriteFrame(writer io.Writer, value any, maxSize int) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode IPC frame: %w", err)
	}
	maxSize = normalizedMaxSize(maxSize)
	if len(payload) > maxSize {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, len(payload), maxSize)
	}
	header := [4]byte{}
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return fmt.Errorf("write IPC frame header: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("write IPC frame payload: %w", err)
	}
	return nil
}

func ReadFrame(reader io.Reader, value any, maxSize int) error {
	header := [4]byte{}
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return fmt.Errorf("read IPC frame header: %w", err)
	}
	size := int(binary.BigEndian.Uint32(header[:]))
	maxSize = normalizedMaxSize(maxSize)
	if size > maxSize {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, size, maxSize)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return fmt.Errorf("read IPC frame payload: %w", err)
	}
	if err := json.Unmarshal(payload, value); err != nil {
		return fmt.Errorf("decode IPC frame: %w", err)
	}
	return nil
}

func normalizedMaxSize(size int) int {
	if size <= 0 {
		return DefaultMaxFrameSize
	}
	return size
}
