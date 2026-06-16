package model

import (
	"encoding/json"
	"strconv"
)

// UnmarshalPoint decodes a single telemetry point. It hand-parses the canonical,
// well-formed object with zero allocations on the hot ingest path, and falls
// back to encoding/json for ANY input the fast path is not fully certain about —
// so the accept/reject behavior is identical to the standard decoder for every
// input (guaranteed by the fallback, verified by FuzzUnmarshalPoint).
func UnmarshalPoint(data []byte, p *TelemetryPoint) error {
	if fastPoint(data, p) {
		return nil
	}
	*p = TelemetryPoint{}
	return json.Unmarshal(data, p)
}

// UnmarshalBatch decodes {"points":[...]} into dst. Same fast-path + fallback
// contract: any deviation hands the whole body to encoding/json.
func UnmarshalBatch(data []byte, dst *[]TelemetryPoint) error {
	if pts, ok := fastBatch(data); ok {
		*dst = pts
		return nil
	}
	*dst = nil
	var v struct {
		Points []TelemetryPoint `json:"points"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*dst = v.Points
	return nil
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// fastPoint parses one canonical object. Returns false (→ caller falls back to
// encoding/json) on anything non-trivial: escapes, unknown keys, non-number
// values, malformed numbers, trailing data, etc.
func fastPoint(data []byte, p *TelemetryPoint) bool {
	var tmp TelemetryPoint
	i := skipWS(data, 0)
	end, ok := parseObject(data, i, &tmp)
	if !ok {
		return false
	}
	if skipWS(data, end) != len(data) {
		return false // trailing non-whitespace → let json decide (it errors)
	}
	*p = tmp
	return true
}

// parseObject reads a "{ ... }" of telemetry fields starting at i, returns the
// index just past '}' and whether it succeeded.
func parseObject(b []byte, i int, p *TelemetryPoint) (int, bool) {
	if i >= len(b) || b[i] != '{' {
		return 0, false
	}
	i = skipWS(b, i+1)
	if i < len(b) && b[i] == '}' {
		return i + 1, true // empty object → all zero (matches json)
	}
	for {
		key, ni, ok := parseKey(b, i)
		if !ok {
			return 0, false
		}
		ni = skipWS(b, ni)
		if ni >= len(b) || b[ni] != ':' {
			return 0, false
		}
		ni = skipWS(b, ni+1)
		ni, ok = parseField(b, ni, key, p)
		if !ok {
			return 0, false
		}
		ni = skipWS(b, ni)
		if ni >= len(b) {
			return 0, false
		}
		switch b[ni] {
		case ',':
			i = skipWS(b, ni+1)
		case '}':
			return ni + 1, true
		default:
			return 0, false
		}
	}
}

// parseKey reads a simple double-quoted key (no escapes). Returns the key bytes,
// the index past the closing quote, and ok.
func parseKey(b []byte, i int) ([]byte, int, bool) {
	if i >= len(b) || b[i] != '"' {
		return nil, 0, false
	}
	i++
	start := i
	for i < len(b) {
		c := b[i]
		if c == '"' {
			return b[start:i], i + 1, true
		}
		if c == '\\' || c < 0x20 {
			return nil, 0, false // escapes/control → fall back
		}
		i++
	}
	return nil, 0, false
}

// parseField parses the value for key and stores it. Unknown keys → false
// (fallback; json ignores unknowns, so delegating keeps parity).
func parseField(b []byte, i int, key []byte, p *TelemetryPoint) (int, bool) {
	switch string(key) {
	case "ts":
		v, ni, ok := parseInt(b, i)
		if !ok {
			return 0, false
		}
		p.Ts = v
		return ni, true
	case "lat":
		return parseFloatInto(b, i, &p.Lat)
	case "lon":
		return parseFloatInto(b, i, &p.Lon)
	case "ax":
		return parseFloatInto(b, i, &p.Ax)
	case "ay":
		return parseFloatInto(b, i, &p.Ay)
	case "az":
		return parseFloatInto(b, i, &p.Az)
	case "battery":
		var f float64
		ni, ok := parseFloatInto(b, i, &f)
		if !ok {
			return 0, false
		}
		p.Battery = &f
		return ni, true
	default:
		return 0, false // unknown key → fall back to encoding/json
	}
}

func parseFloatInto(b []byte, i int, dst *float64) (int, bool) {
	tok, ni, ok := numberToken(b, i)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(tok), 64)
	if err != nil {
		return 0, false
	}
	*dst = f
	return ni, true
}

// parseInt accepts only a JSON integer (no '.', 'e'); anything else → false so
// the fallback (encoding/json) reproduces its int64 rejection behavior.
func parseInt(b []byte, i int) (int64, int, bool) {
	tok, ni, ok := numberToken(b, i)
	if !ok {
		return 0, 0, false
	}
	for _, c := range tok {
		if c == '.' || c == 'e' || c == 'E' {
			return 0, 0, false
		}
	}
	v, err := strconv.ParseInt(string(tok), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return v, ni, true
}

// numberToken extracts a strictly-valid JSON number token starting at i. It
// enforces the JSON number grammar (no leading '+', no leading zeros, digit
// required after '.', etc.) so it never accepts a number encoding/json rejects.
func numberToken(b []byte, i int) ([]byte, int, bool) {
	start := i
	n := len(b)
	if i < n && b[i] == '-' {
		i++
	}
	// integer part
	if i >= n {
		return nil, 0, false
	}
	if b[i] == '0' {
		i++
	} else if b[i] >= '1' && b[i] <= '9' {
		for i < n && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	} else {
		return nil, 0, false
	}
	// fraction
	if i < n && b[i] == '.' {
		i++
		if i >= n || b[i] < '0' || b[i] > '9' {
			return nil, 0, false
		}
		for i < n && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	// exponent
	if i < n && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < n && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= n || b[i] < '0' || b[i] > '9' {
			return nil, 0, false
		}
		for i < n && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	return b[start:i], i, true
}

// fastBatch parses {"points":[ obj, obj, ... ]} with one slice allocation.
func fastBatch(data []byte) ([]TelemetryPoint, bool) {
	i := skipWS(data, 0)
	if i >= len(data) || data[i] != '{' {
		return nil, false
	}
	i = skipWS(data, i+1)
	key, ni, ok := parseKey(data, i)
	if !ok || string(key) != "points" {
		return nil, false
	}
	i = skipWS(data, ni)
	if i >= len(data) || data[i] != ':' {
		return nil, false
	}
	i = skipWS(data, i+1)
	if i >= len(data) || data[i] != '[' {
		return nil, false
	}
	i = skipWS(data, i+1)
	pts := []TelemetryPoint{} // non-nil to match encoding/json on "[]"
	if i < len(data) && data[i] == ']' {
		i++ // empty array
	} else {
		for {
			var p TelemetryPoint
			ni, ok := parseObject(data, i, &p)
			if !ok {
				return nil, false
			}
			pts = append(pts, p)
			i = skipWS(data, ni)
			if i >= len(data) {
				return nil, false
			}
			if data[i] == ',' {
				i = skipWS(data, i+1)
				continue
			}
			if data[i] == ']' {
				i++
				break
			}
			return nil, false
		}
	}
	// after ']': optional ws, then '}'
	i = skipWS(data, i)
	if i >= len(data) || data[i] != '}' {
		return nil, false
	}
	if skipWS(data, i+1) != len(data) {
		return nil, false
	}
	return pts, true
}
