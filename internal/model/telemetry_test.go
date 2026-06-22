package model

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppendJSONParity asserts the hand-rolled encoder produces JSON that
// decodes to exactly what encoding/json would have produced (byte-identity is
// not required; value identity is).
func TestAppendJSONParity(t *testing.T) {
	bat := 0.83
	for _, p := range []TelemetryPoint{
		{Ts: 1718800000000, Lat: -23.55, Lon: -46.63, Battery: &bat, Ax: 0.1, Ay: -0.04, Az: 9.81},
		{Ts: 1, Lat: 0, Lon: 0, Ax: 0, Ay: 0, Az: 0},        // nil battery, all zeros
		{Ts: 2, Lat: 90, Lon: -180, Ax: -1.5, Ay: 2.25, Az: 9.806},
	} {
		hand := p.AppendJSON(nil)
		std, err := json.Marshal(p)
		require.NoError(t, err)

		var fromHand, fromStd TelemetryPoint
		require.NoError(t, json.Unmarshal(hand, &fromHand), "hand=%s", hand)
		require.NoError(t, json.Unmarshal(std, &fromStd))
		assert.Equal(t, fromStd, fromHand, "hand=%s std=%s", hand, std)
	}
}

func benchPoints(n int) []TelemetryPoint {
	bat := 0.81
	pts := make([]TelemetryPoint, n)
	for i := range pts {
		pts[i] = TelemetryPoint{Ts: int64(1718800000000 + i), Lat: -23.55, Lon: -46.63,
			Battery: &bat, Ax: 0.1, Ay: -0.04, Az: 9.81}
	}
	return pts
}

// BenchmarkRangeStdJSON is the old reflection path (json.Marshal of the slice).
func BenchmarkRangeStdJSON(b *testing.B) {
	pts := benchPoints(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(pts)
	}
}

// BenchmarkRangeAppendJSON is the hand-rolled path.
func BenchmarkRangeAppendJSON(b *testing.B) {
	pts := benchPoints(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := make([]byte, 0, 50*96)
		for j := range pts {
			buf = pts[j].AppendJSON(buf)
		}
		_ = buf
	}
}

func TestEncodeProduces56Bytes(t *testing.T) {
	bat := 0.5
	p := TelemetryPoint{Ts: 1234, Lat: 12.5, Lon: -45.25, Battery: &bat, Ax: 1, Ay: 2, Az: 3}
	b := p.Encode()
	require.Len(t, b, 56)
}

func TestEncodeDecodeRoundTripWithBattery(t *testing.T) {
	bat := 0.75
	p := TelemetryPoint{Ts: 99887766, Lat: -89.5, Lon: 179.5, Battery: &bat, Ax: 0.1, Ay: -0.2, Az: 9.81}
	got := DecodePoint(p.Encode())

	assert.Equal(t, int64(99887766), got.Ts)
	assert.Equal(t, -89.5, got.Lat)
	assert.Equal(t, 179.5, got.Lon)
	require.NotNil(t, got.Battery)
	assert.Equal(t, 0.75, *got.Battery)
	assert.Equal(t, 0.1, got.Ax)
	assert.Equal(t, -0.2, got.Ay)
	assert.Equal(t, 9.81, got.Az)
}

func TestEncodeNilBatteryDecodesToNil(t *testing.T) {
	p := TelemetryPoint{Ts: 1, Lat: 0, Lon: 0, Battery: nil, Ax: 1, Ay: 1, Az: 1}
	b := p.Encode()

	// battery slot must hold NaN in the binary
	raw := math.Float64frombits(uint64(b[24]) | uint64(b[25])<<8 | uint64(b[26])<<16 | uint64(b[27])<<24 |
		uint64(b[28])<<32 | uint64(b[29])<<40 | uint64(b[30])<<48 | uint64(b[31])<<56)
	assert.True(t, math.IsNaN(raw), "absent battery must encode as NaN")

	got := DecodePoint(b)
	assert.Nil(t, got.Battery, "NaN battery must decode to nil pointer")
}
