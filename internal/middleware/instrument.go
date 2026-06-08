package middleware

import (
	"time"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/metrics"
	"github.com/gofiber/fiber/v2"
)

// Instrument stamps every API response with X-Instance-Id and records request
// latency for handlers that declare an op via c.Locals("op", ...). Health,
// readiness, and metrics routes set no op and are therefore not observed.
func Instrument(instanceID string, m *metrics.Metrics) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("X-Instance-Id", instanceID)
		start := time.Now()
		err := c.Next()
		if op, ok := c.Locals("op").(string); ok {
			m.Observe(op, time.Since(start).Seconds())
		}
		return err
	}
}
