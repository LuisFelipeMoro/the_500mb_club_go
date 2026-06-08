package model

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
