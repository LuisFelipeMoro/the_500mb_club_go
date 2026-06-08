package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRoundTrip(t *testing.T) {
	c := Cursor{TS: 1717000000123, Offset: 7}
	enc := EncodeCursor(c)
	require.Len(t, enc, 22, "16 bytes base64url (no padding) = 22 chars")

	got, err := DecodeCursor(enc)
	require.NoError(t, err)
	assert.Equal(t, c, got)
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	_, err := DecodeCursor("!!!notbase64!!!")
	assert.Error(t, err)

	_, err = DecodeCursor("YWJj") // valid base64 but only 3 bytes
	assert.Error(t, err)
}

func TestNextCursorNoMoreResults(t *testing.T) {
	// fewer results than limit+1 means this is the last page
	assert.Nil(t, NextCursor([]int64{10, 20, 30}, 5, nil))
	assert.Nil(t, NextCursor([]int64{10, 20, 30}, 3, nil))
}

func TestNextCursorDistinctTimestamps(t *testing.T) {
	// limit 3, 4 results -> more pages. last in-page ts is results[2]=30, offset 1.
	c := NextCursor([]int64{10, 20, 30, 40}, 3, nil)
	require.NotNil(t, c)
	assert.Equal(t, int64(30), c.TS)
	assert.Equal(t, int64(1), c.Offset)
}

// The critical case: many points sharing one ts must paginate without skips or
// duplicates across page boundaries.
func TestPaginationTieSafe(t *testing.T) {
	// 10 points all at ts=1000, plus one at ts=2000.
	all := make([]int64, 0, 11)
	for i := 0; i < 10; i++ {
		all = append(all, 1000)
	}
	all = append(all, 2000)

	const limit = 3
	var prev *Cursor
	var seen []int64

	for {
		// Simulate ZRANGE key <from> <to> BYSCORE LIMIT <offset> <limit+1>:
		// "from" is prev.TS (or the window start), skipping prev.Offset items
		// whose ts == ... actually skipping the first Offset items at/after from.
		fromTS := int64(0)
		offset := 0
		if prev != nil {
			fromTS = prev.TS
			offset = int(prev.Offset)
		}
		var window []int64
		for _, ts := range all {
			if ts >= fromTS {
				window = append(window, ts)
			}
		}
		if offset > len(window) {
			offset = len(window)
		}
		window = window[offset:]
		take := limit + 1
		if take > len(window) {
			take = len(window)
		}
		results := window[:take]

		page := results
		if len(results) > limit {
			page = results[:limit]
		}
		seen = append(seen, page...)

		prev = NextCursor(results, limit, prev)
		if prev == nil {
			break
		}
	}

	assert.Equal(t, all, seen, "every point returned exactly once, in order")
}
