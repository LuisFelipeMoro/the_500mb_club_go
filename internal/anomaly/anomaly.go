package anomaly

import (
	"math"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
)

// Result is the anomaly response payload. All three fields are required.
type Result struct {
	ZScore    float64 `json:"z_score"`
	Samples   int     `json:"samples"`
	Anomalous bool    `json:"anomalous"`
}

// Magnitude is the Euclidean norm of the acceleration vector.
func Magnitude(p model.TelemetryPoint) float64 {
	return math.Sqrt(p.Ax*p.Ax + p.Ay*p.Ay + p.Az*p.Az)
}

// Compute returns the z-score of the newest point's acceleration magnitude
// against the mean/stddev of the whole window. window[0] is the newest point.
// A zero stddev yields a z-score of 0 (not anomalous) to avoid division by zero.
func Compute(window []model.TelemetryPoint) Result {
	n := len(window)
	r := Result{Samples: n}
	if n == 0 {
		return r
	}

	newest := Magnitude(window[0])

	var sum float64
	mags := make([]float64, n)
	for i := range window {
		m := Magnitude(window[i])
		mags[i] = m
		sum += m
	}
	mean := sum / float64(n)

	var variance float64
	for _, m := range mags {
		d := m - mean
		variance += d * d
	}
	variance /= float64(n)
	stddev := math.Sqrt(variance)

	if stddev == 0 {
		return r // ZScore 0, Anomalous false
	}

	r.ZScore = (newest - mean) / stddev
	r.Anomalous = math.Abs(r.ZScore) > 3
	return r
}
