package anomaly

import (
	"testing"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestMagnitude(t *testing.T) {
	assert.InDelta(t, 5.0, Magnitude(model.TelemetryPoint{Ax: 3, Ay: 4, Az: 0}), 1e-9)
	assert.InDelta(t, 0.0, Magnitude(model.TelemetryPoint{Ax: 0, Ay: 0, Az: 0}), 1e-9)
}

func mag(az float64) model.TelemetryPoint {
	return model.TelemetryPoint{Ts: 1, Ax: 0, Ay: 0, Az: az}
}

func TestComputeZeroStddev(t *testing.T) {
	// all identical magnitudes -> stddev 0 -> guard returns z=0, not anomalous
	win := []model.TelemetryPoint{mag(5), mag(5), mag(5), mag(5), mag(5), mag(5), mag(5), mag(5)}
	r := Compute(win)
	assert.Equal(t, 0.0, r.ZScore)
	assert.False(t, r.Anomalous)
	assert.Equal(t, 8, r.Samples)
}

func TestComputeAnomalous(t *testing.T) {
	// newest (window[0]) is a large outlier; 10 baseline points at 0.
	win := []model.TelemetryPoint{mag(100)}
	for i := 0; i < 10; i++ {
		win = append(win, mag(0))
	}
	r := Compute(win)
	// mean=100/11, population stddev computed over all 11 -> z ~ 3.16
	assert.InDelta(t, 3.162, r.ZScore, 0.01)
	assert.True(t, r.Anomalous, "|z|>3 must be anomalous")
	assert.Equal(t, 11, r.Samples)
}

func TestComputeNotAnomalousAtThreshold(t *testing.T) {
	// constructed so |z| == 3.0 exactly -> NOT anomalous (strict >)
	win := []model.TelemetryPoint{mag(100)}
	for i := 0; i < 9; i++ {
		win = append(win, mag(0))
	}
	r := Compute(win)
	assert.InDelta(t, 3.0, r.ZScore, 1e-9)
	assert.False(t, r.Anomalous, "z exactly 3 is not > 3")
}
