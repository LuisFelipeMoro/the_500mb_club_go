package model

import (
	"encoding/json"
	"testing"
)

var singleJSON = []byte(`{"ts":1715800000000,"lat":-23.5505,"lon":-46.6333,"battery":0.82,"ax":0.11,"ay":-0.04,"az":9.81}`)

func batchJSON(n int) []byte {
	b := []byte(`{"points":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, singleJSON...)
	}
	return append(b, ']', '}')
}

func BenchmarkJSONUnmarshalSingle(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var p TelemetryPoint
		if err := json.Unmarshal(singleJSON, &p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJSONUnmarshalBatch50(b *testing.B) {
	body := batchJSON(50)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var v struct {
			Points []TelemetryPoint `json:"points"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			b.Fatal(err)
		}
	}
}
