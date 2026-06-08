package validate

import (
	"errors"
	"math"
	"regexp"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
)

var deviceIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// DeviceID reports whether id matches ^[a-zA-Z0-9_-]{1,64}$.
func DeviceID(id string) bool { return deviceIDRe.MatchString(id) }

// Point validates a single TelemetryPoint's field constraints.
func Point(p model.TelemetryPoint) error {
	if p.Ts <= 0 {
		return errors.New("ts must be a positive epoch-millis value")
	}
	if p.Lat < -90 || p.Lat > 90 {
		return errors.New("lat out of range [-90, 90]")
	}
	if p.Lon < -180 || p.Lon > 180 {
		return errors.New("lon out of range [-180, 180]")
	}
	if p.Battery != nil && (*p.Battery < 0 || *p.Battery > 1) {
		return errors.New("battery out of range [0, 1]")
	}
	if !isFinite(p.Ax) || !isFinite(p.Ay) || !isFinite(p.Az) {
		return errors.New("ax/ay/az must be finite")
	}
	return nil
}

// Points validates every point; the first failure rejects the whole batch.
func Points(pts []model.TelemetryPoint) error {
	for i := range pts {
		if err := Point(pts[i]); err != nil {
			return err
		}
	}
	return nil
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
