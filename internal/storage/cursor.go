package storage

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
)

// Cursor marks a position in a time-ordered range scan. Offset counts how many
// items sharing Cursor.TS have already been returned, making pagination safe
// across same-timestamp tie groups.
type Cursor struct {
	TS     int64
	Offset int64
}

// EncodeCursor renders a Cursor as base64url(LE uint64 TS ++ LE uint64 Offset).
func EncodeCursor(c Cursor) string {
	var b [16]byte
	binary.LittleEndian.PutUint64(b[0:8], uint64(c.TS))
	binary.LittleEndian.PutUint64(b[8:16], uint64(c.Offset))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// DecodeCursor parses a cursor string, erroring on malformed input.
func DecodeCursor(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, err
	}
	if len(b) != 16 {
		return Cursor{}, errors.New("cursor must decode to 16 bytes")
	}
	return Cursor{
		TS:     int64(binary.LittleEndian.Uint64(b[0:8])),
		Offset: int64(binary.LittleEndian.Uint64(b[8:16])),
	}, nil
}

// NextCursor returns the cursor for the following page, or nil if results
// (length up to limit+1) did not overflow the page. resultTS holds the
// timestamps of the fetched items in ascending order.
func NextCursor(resultTS []int64, limit int, prev *Cursor) *Cursor {
	if len(resultTS) <= limit {
		return nil
	}
	lastTS := resultTS[limit-1]
	sameTS := int64(0)
	for _, ts := range resultTS[:limit] {
		if ts == lastTS {
			sameTS++
		}
	}
	offset := sameTS
	if prev != nil && prev.Offset > 0 && lastTS == prev.TS {
		offset = prev.Offset + sameTS
	}
	return &Cursor{TS: lastTS, Offset: offset}
}
