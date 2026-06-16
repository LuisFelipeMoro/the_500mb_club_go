package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

// pointParity asserts UnmarshalPoint accepts/rejects and decodes a value
// identically to encoding/json for the given input.
func pointParity(t *testing.T, in string) {
	t.Helper()
	var fast, std TelemetryPoint
	errFast := UnmarshalPoint([]byte(in), &fast)
	errStd := json.Unmarshal([]byte(in), &std)
	if (errFast == nil) != (errStd == nil) {
		t.Fatalf("error mismatch for %q: fast=%v std=%v", in, errFast, errStd)
	}
	if errStd == nil && !reflect.DeepEqual(fast, std) {
		t.Fatalf("value mismatch for %q: fast=%+v std=%+v", in, fast, std)
	}
}

func TestUnmarshalPointParity(t *testing.T) {
	cases := []string{
		`{"ts":1715800000000,"lat":-23.5505,"lon":-46.6333,"battery":0.82,"ax":0.11,"ay":-0.04,"az":9.81}`,
		`{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`,                // no battery
		`  {  "ts" : 1 , "lat":0,"lon":0,"ax":0,"ay":0,"az":9.8 }  `,     // whitespace
		`{"az":9.8,"ay":0,"ax":0,"lon":0,"lat":0,"ts":1}`,                // reordered
		`{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8,"extra":42}`,     // unknown field
		`{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8,"battery":null}`, // null battery
		`{"ts":1,"lat":1e2,"lon":-1.5E-3,"ax":0,"ay":0,"az":9.8}`,        // exponents
		`{}`, // empty object
		`{"ts":1.5,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`,  // float ts -> json errors
		`{"ts":01,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`,   // leading zero -> invalid
		`{"ts":+1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`,   // leading + -> invalid
		`{"ts":1,"lat":.5,"lon":0,"ax":0,"ay":0,"az":9.8}`,   // .5 -> invalid
		`{"ts":1,"lat":"x","lon":0,"ax":0,"ay":0,"az":9.8}`,  // string value
		`{"ts":1,"lat":true,"lon":0,"ax":0,"ay":0,"az":9.8}`, // bool value
		`{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8`,     // unterminated
		`{"ts":1}trailing`, // trailing data
		`{"ts":99999999999999999999999,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`, // int64 overflow
		`[]`,        // not an object
		`null`,      // null
		`{"ts":-5}`, // negative ts (valid json; validate.Point rejects later)
		``,          // empty input
		`   `,       // only whitespace
	}
	for _, c := range cases {
		pointParity(t, c)
	}
}

func batchParity(t *testing.T, in string) {
	t.Helper()
	var fast []TelemetryPoint
	errFast := UnmarshalBatch([]byte(in), &fast)
	var std struct {
		Points []TelemetryPoint `json:"points"`
	}
	errStd := json.Unmarshal([]byte(in), &std)
	if (errFast == nil) != (errStd == nil) {
		t.Fatalf("error mismatch for %q: fast=%v std=%v", in, errFast, errStd)
	}
	if errStd == nil && !reflect.DeepEqual(fast, std.Points) {
		t.Fatalf("value mismatch for %q: fast=%+v std=%+v", in, fast, std.Points)
	}
}

func TestUnmarshalBatchParity(t *testing.T) {
	cases := []string{
		`{"points":[{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}]}`,
		`{"points":[{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8},{"ts":2,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}]}`,
		`{"points":[]}`,
		`  { "points" : [ { "ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8 } ] } `,
		`{"points":[{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8,"battery":0.5}]}`,
		`{"points":[{"ts":1.5}]}`,         // invalid point inside
		`{"points":[{"ts":1}],"extra":1}`, // trailing field after array
		`{"points":[{"ts":1},]}`,          // trailing comma
		`{"points":{}}`,                   // points not an array
		`{"items":[]}`,                    // wrong wrapper key
		`{"points":[{"ts":1}`,             // unterminated
		`{}`,                              // missing points
		``,
	}
	for _, c := range cases {
		batchParity(t, c)
	}
}

// FuzzUnmarshalPoint: for any input, UnmarshalPoint must match encoding/json on
// accept/reject and (when accepted) value. The fallback makes this hold by
// construction; the fuzzer guards the fast path against silent divergence.
func FuzzUnmarshalPoint(f *testing.F) {
	for _, s := range []string{
		`{"ts":1,"lat":0,"lon":0,"ax":0,"ay":0,"az":9.8}`,
		`{"ts":1.5}`, `{"battery":0.5}`, `{}`, `[]`, `null`, `{"ts":01}`, `{"x":1}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var fast, std TelemetryPoint
		errFast := UnmarshalPoint(data, &fast)
		errStd := json.Unmarshal(data, &std)
		if (errFast == nil) != (errStd == nil) {
			t.Fatalf("error mismatch for %q: fast=%v std=%v", data, errFast, errStd)
		}
		if errStd == nil && !reflect.DeepEqual(fast, std) {
			t.Fatalf("value mismatch for %q: fast=%+v std=%+v", data, fast, std)
		}
	})
}

func BenchmarkUnmarshalPointFast(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var p TelemetryPoint
		if err := UnmarshalPoint(singleJSON, &p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalBatchFast50(b *testing.B) {
	body := batchJSON(50)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var pts []TelemetryPoint
		if err := UnmarshalBatch(body, &pts); err != nil {
			b.Fatal(err)
		}
	}
}
