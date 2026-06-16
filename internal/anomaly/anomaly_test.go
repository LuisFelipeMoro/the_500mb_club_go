package anomaly

import (
	"math/rand"
	"testing"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
	"github.com/stretchr/testify/assert"
)

// encode builds an encoded member with the given acceleration components.
func encode(ax, ay, az float64) []byte {
	return model.TelemetryPoint{Ts: 1, Ax: ax, Ay: ay, Az: az}.Encode()
}

// ComputeMembers (raw byte path) must match Compute (decoded path) exactly.
func TestComputeMembersParity(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, n := range []int{8, 11, 100, 256} {
		win := make([]model.TelemetryPoint, n)
		members := make([][]byte, n)
		for i := range win {
			ax, ay, az := rng.Float64()*4-2, rng.Float64()*4-2, 9.8+rng.Float64()*2
			win[i] = model.TelemetryPoint{Ts: int64(i + 1), Ax: ax, Ay: ay, Az: az}
			members[i] = win[i].Encode()
		}
		want, got := Compute(win), ComputeMembers(members)
		assert.Equal(t, want.Samples, got.Samples, "n=%d samples", n)
		assert.Equal(t, want.Anomalous, got.Anomalous, "n=%d anomalous", n)
		assert.InDelta(t, want.ZScore, got.ZScore, 1e-9, "n=%d zscore", n)
	}
}

func TestComputeMembersZeroStddev(t *testing.T) {
	members := make([][]byte, 8)
	for i := range members {
		members[i] = encode(0, 0, 5)
	}
	r := ComputeMembers(members)
	assert.Equal(t, 0.0, r.ZScore)
	assert.False(t, r.Anomalous)
	assert.Equal(t, 8, r.Samples)
}

func TestComputeMembersEmpty(t *testing.T) {
	r := ComputeMembers(nil)
	assert.Equal(t, 0, r.Samples)
	assert.False(t, r.Anomalous)
}

func benchMembers(n int) [][]byte {
	members := make([][]byte, n)
	for i := range members {
		members[i] = encode(0.1, -0.04, 9.81+float64(i%5)*0.01)
	}
	return members
}

// BenchmarkComputeDecoded mirrors the old handler path (decode then Compute).
func BenchmarkComputeDecoded(b *testing.B) {
	members := benchMembers(256)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		win := make([]model.TelemetryPoint, len(members))
		for j, m := range members {
			win[j] = model.DecodePoint(m)
		}
		_ = Compute(win)
	}
}

// BenchmarkComputeMembers is the new allocation-free hot path.
func BenchmarkComputeMembers(b *testing.B) {
	members := benchMembers(256)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeMembers(members)
	}
}

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
