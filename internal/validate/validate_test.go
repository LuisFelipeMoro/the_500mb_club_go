package validate

import (
	"math"
	"testing"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestDeviceID(t *testing.T) {
	assert.True(t, DeviceID("abc"))
	assert.True(t, DeviceID("Dev_01-XYZ"))
	assert.True(t, DeviceID("a"))
	assert.True(t, DeviceID(string(make([]byte, 0))+repeat("a", 64)))

	assert.False(t, DeviceID(""), "empty rejected")
	assert.False(t, DeviceID(repeat("a", 65)), "65 chars rejected")
	assert.False(t, DeviceID("has space"))
	assert.False(t, DeviceID("has/slash"))
	assert.False(t, DeviceID("dot.dot"))
}

func TestValidPoint(t *testing.T) {
	bat := 0.5
	p := model.TelemetryPoint{Ts: 1, Lat: 0, Lon: 0, Battery: &bat, Ax: 1, Ay: 1, Az: 1}
	assert.NoError(t, Point(p))
}

func TestPointNilBatteryOK(t *testing.T) {
	p := model.TelemetryPoint{Ts: 1, Lat: 0, Lon: 0, Battery: nil, Ax: 1, Ay: 1, Az: 1}
	assert.NoError(t, Point(p))
}

func TestPointRejectsBadValues(t *testing.T) {
	base := func() model.TelemetryPoint {
		return model.TelemetryPoint{Ts: 1, Lat: 0, Lon: 0, Ax: 1, Ay: 1, Az: 1}
	}

	bad := base()
	bad.Ts = 0
	assert.Error(t, Point(bad), "ts must be positive")

	bad = base()
	bad.Ts = -5
	assert.Error(t, Point(bad))

	bad = base()
	bad.Lat = 90.1
	assert.Error(t, Point(bad), "lat > 90")
	bad.Lat = -90.1
	assert.Error(t, Point(bad), "lat < -90")

	bad = base()
	bad.Lon = 180.1
	assert.Error(t, Point(bad), "lon > 180")
	bad.Lon = -180.1
	assert.Error(t, Point(bad), "lon < -180")

	bad = base()
	b := 1.1
	bad.Battery = &b
	assert.Error(t, Point(bad), "battery > 1")
	b2 := -0.1
	bad.Battery = &b2
	assert.Error(t, Point(bad), "battery < 0")

	bad = base()
	bad.Ax = math.NaN()
	assert.Error(t, Point(bad), "ax NaN")
	bad.Ax = math.Inf(1)
	assert.Error(t, Point(bad), "ax Inf")

	bad = base()
	bad.Ay = math.NaN()
	assert.Error(t, Point(bad))

	bad = base()
	bad.Az = math.Inf(-1)
	assert.Error(t, Point(bad))
}

func TestPoints(t *testing.T) {
	good := model.TelemetryPoint{Ts: 1, Lat: 0, Lon: 0, Ax: 1, Ay: 1, Az: 1}
	assert.NoError(t, Points([]model.TelemetryPoint{good, good}))

	bad := good
	bad.Lat = 91
	assert.Error(t, Points([]model.TelemetryPoint{good, bad}), "one invalid point fails all")
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
