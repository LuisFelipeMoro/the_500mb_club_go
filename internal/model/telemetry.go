package model

import (
	"encoding/binary"
	"math"
)

// EncodedSize is the fixed width of a serialized TelemetryPoint.
const EncodedSize = 56

// TelemetryPoint is an ingested GPS+sensor sample. Battery is optional;
// nil means absent and is encoded as NaN in the 56-byte binary form.
type TelemetryPoint struct {
	Ts      int64    `json:"ts"`
	Lat     float64  `json:"lat"`
	Lon     float64  `json:"lon"`
	Battery *float64 `json:"battery,omitempty"`
	Ax      float64  `json:"ax"`
	Ay      float64  `json:"ay"`
	Az      float64  `json:"az"`
}

// Encode serializes the point into a 56-byte little-endian blob:
//
//	[0:8] ts | [8:16] lat | [16:24] lon | [24:32] battery (NaN=absent)
//	[32:40] ax | [40:48] ay | [48:56] az
func (p TelemetryPoint) Encode() []byte {
	b := make([]byte, EncodedSize)
	binary.LittleEndian.PutUint64(b[0:8], uint64(p.Ts))
	binary.LittleEndian.PutUint64(b[8:16], math.Float64bits(p.Lat))
	binary.LittleEndian.PutUint64(b[16:24], math.Float64bits(p.Lon))
	battery := math.NaN()
	if p.Battery != nil {
		battery = *p.Battery
	}
	binary.LittleEndian.PutUint64(b[24:32], math.Float64bits(battery))
	binary.LittleEndian.PutUint64(b[32:40], math.Float64bits(p.Ax))
	binary.LittleEndian.PutUint64(b[40:48], math.Float64bits(p.Ay))
	binary.LittleEndian.PutUint64(b[48:56], math.Float64bits(p.Az))
	return b
}

// AccelMagnitude reads ax/ay/az straight from an encoded 56-byte member and
// returns sqrt(ax²+ay²+az²). It lets the anomaly hot path compute magnitudes
// without allocating a TelemetryPoint per member (no battery/lat/lon needed).
func AccelMagnitude(b []byte) float64 {
	ax := math.Float64frombits(binary.LittleEndian.Uint64(b[32:40]))
	ay := math.Float64frombits(binary.LittleEndian.Uint64(b[40:48]))
	az := math.Float64frombits(binary.LittleEndian.Uint64(b[48:56]))
	return math.Sqrt(ax*ax + ay*ay + az*az)
}

// DecodePoint reverses Encode. A NaN battery slot decodes to a nil pointer.
func DecodePoint(b []byte) TelemetryPoint {
	p := TelemetryPoint{
		Ts:  int64(binary.LittleEndian.Uint64(b[0:8])),
		Lat: math.Float64frombits(binary.LittleEndian.Uint64(b[8:16])),
		Lon: math.Float64frombits(binary.LittleEndian.Uint64(b[16:24])),
		Ax:  math.Float64frombits(binary.LittleEndian.Uint64(b[32:40])),
		Ay:  math.Float64frombits(binary.LittleEndian.Uint64(b[40:48])),
		Az:  math.Float64frombits(binary.LittleEndian.Uint64(b[48:56])),
	}
	if bat := math.Float64frombits(binary.LittleEndian.Uint64(b[24:32])); !math.IsNaN(bat) {
		p.Battery = &bat
	}
	return p
}
