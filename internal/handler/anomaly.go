package handler

import (
	"context"
	"errors"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/anomaly"
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

	ctx, cancel := context.WithTimeout(c.UserContext(), h.ReadTimeout)
	defer cancel()
	members, err := h.Store.LastN(ctx, id, 256)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			h.Metrics.ReadTimeouts.Inc()
		}
		h.Log.Error("anomaly query failed", zap.Error(err))
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	if len(members) < 8 {
		h.Metrics.AnomalyInsufficient.Inc()
		return c.SendStatus(fiber.StatusNotFound)
	}

	return c.Status(fiber.StatusOK).JSON(anomaly.ComputeMembers(members))
}
