package handler

import (
	"context"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/anomaly"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/validate"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// GetAnomaly handles GET /devices/{id}/anomaly (FR-4): z-score of the newest
// acceleration magnitude over the last 256 points. 404 if fewer than 8. No cache.
func (h *Handler) GetAnomaly(c *fiber.Ctx) error {
	c.Locals("op", "anomaly")
	id := c.Params("id")
	if !validate.DeviceID(id) {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	members, err := h.Store.LastN(context.Background(), id, 256)
	if err != nil {
		h.Log.Error("anomaly query failed", zap.Error(err))
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	if len(members) < 8 {
		h.Metrics.AnomalyInsufficient.Inc()
		return c.SendStatus(fiber.StatusNotFound)
	}

	window := make([]model.TelemetryPoint, len(members))
	for i, m := range members {
		window[i] = model.DecodePoint(m)
	}
	return c.Status(fiber.StatusOK).JSON(anomaly.Compute(window))
}
