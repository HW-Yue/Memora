package native

import (
	"encoding/binary"
	"fmt"
	"os"
)

// The commit hint is how a Database is opened without reading it.
//
// A record log has one thing an open genuinely has to establish: where the last
// committed transaction ends. Not for the data — the readers walk for that —
// but because a crash can leave a half-written record at the end of the file,
// and appending past one would bury it in the middle, where it stops every
// later read. So the tail has to be cut off while it is still the tail.
//
// Finding that boundary by reading the whole log is what E8 stage 3 exists to
// delete. The hint is a small file beside the log holding the last boundary
// this process saw, so the open walks forward from there — one transaction's
// worth of bytes in the ordinary case — instead of from the beginning.
//
// It is a hint and nothing more. It is written without a sync of its own, it
// may be stale, it may be missing, and the open falls back to walking the whole
// log when it is. What it must never be is *ahead* of the log: the log only
// ever appends, so a file shorter than the hint means the hint is describing
// writes that did not survive, and that is checked for.
const (
	hintSuffix  = ".commit"
	hintSize    = 16
	hintVersion = uint64(1)
	// hintInterval is how many bytes of log may be written before the hint is
	// flushed again. It bounds the walk a crash costs, and it is the only thing
	// the number affects: the hint is never read for correctness.
	hintInterval = int64(1) << 20
)

var hintMagic = [8]byte{'M', 'E', 'M', 'C', 'M', 'T', 0, 1}

func hintPath(logPath string) string { return logPath + hintSuffix }

// readCommitHint returns the boundary the hint claims, or the header size when
// there is no usable hint.
func readCommitHint(logPath string, fileSize int64) int64 {
	encoded, err := os.ReadFile(hintPath(logPath))
	if err != nil || len(encoded) != hintSize {
		return int64(fileHeaderSize)
	}
	if string(encoded[:8]) != string(hintMagic[:]) {
		return int64(fileHeaderSize)
	}
	offset := int64(binary.LittleEndian.Uint64(encoded[8:]))
	// Below the header it is meaningless; above the file it describes writes
	// that did not survive the crash, and walking from there would skip records
	// that did.
	if offset < int64(fileHeaderSize) || offset > fileSize {
		return int64(fileHeaderSize)
	}
	return offset
}

// writeCommitHint replaces the hint file.
//
// Best effort by design: a failure here costs a longer walk after the next
// crash and nothing else, so it must not fail the write it follows. It is
// written whole through a rename so a torn hint is never read as a good one.
func writeCommitHint(logPath string, offset int64) error {
	var encoded [hintSize]byte
	copy(encoded[:8], hintMagic[:])
	binary.LittleEndian.PutUint64(encoded[8:], uint64(offset))
	temporary := hintPath(logPath) + ".next"
	if err := os.WriteFile(temporary, encoded[:], 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, hintPath(logPath)); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// recoverCommitted finds where the last committed transaction ends and cuts off
// anything after it.
//
// The walk starts at the hint. It reads record headers and hops — no payloads,
// no checksums, no map — because all it is establishing is framing: which
// offsets are record boundaries, and which of them a commit mark sits on.
//
// Anything past the last commit mark is a transaction that never committed:
// either a crash mid-append, or a prepare the page Trees refused. Both are
// writes that did not happen, and both are removed here so the next append
// starts on a boundary.
func (f *File) recoverCommitted(logPath string, fileSize int64) error {
	from := readCommitHint(logPath, fileSize)
	committed, err := f.lastCommitBoundary(from, fileSize)
	if err != nil {
		// The hint pointed somewhere that does not frame. Fall back to the
		// whole log, which is what every open used to do.
		if from != int64(fileHeaderSize) {
			committed, err = f.lastCommitBoundary(int64(fileHeaderSize), fileSize)
		}
		if err != nil {
			return err
		}
	}
	if committed < fileSize {
		if err := f.file.Truncate(committed); err != nil {
			return fmt.Errorf("truncate native crash tail: %w", err)
		}
		if err := f.file.Sync(); err != nil {
			return fmt.Errorf("sync native crash recovery: %w", err)
		}
	}
	f.committed = committed
	f.hinted = committed
	return nil
}

// lastCommitBoundary hops record headers from start and reports the offset just
// past the last commit mark, or start itself when there is none.
//
// A record whose header does not decode, or that claims to run past the end of
// the file, ends the walk: that is where the crash landed. It is not reported
// as corruption, because at the end of a log it is not corruption — Verify is
// what reads the log for that.
func (f *File) lastCommitBoundary(start, fileSize int64) (int64, error) {
	if start < int64(fileHeaderSize) {
		start = int64(fileHeaderSize)
	}
	committed := start
	framed := false
	inTransaction := false
	f.recoveredRecords = 0
	for offset := start; offset < fileSize; {
		if fileSize-offset < recordHeaderSize {
			break
		}
		var encoded [recordHeaderSize]byte
		if _, err := f.file.ReadAt(encoded[:], offset); err != nil {
			return 0, fmt.Errorf("read native record header at offset %d: %w", offset, err)
		}
		header, err := decodeRecordHeader(encoded[:])
		if err != nil {
			if !framed {
				return 0, err
			}
			break
		}
		if int64(header.recordLength) > fileSize-offset {
			break
		}
		framed = true
		f.recoveredRecords++
		offset += int64(header.recordLength)
		switch {
		case header.kind == objectKindTransactionBegin:
			// A BEGIN while one is open is the earlier one being abandoned,
			// which Prepare/Complete made an ordinary outcome. Either way what
			// follows is uncommitted until its own mark.
			inTransaction = true
		case header.kind == objectKindTransactionCommit:
			inTransaction = false
			committed = offset
		case !inTransaction:
			// A standalone Put is committed the moment it is written.
			committed = offset
		}
	}
	return committed, nil
}
