package handler

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
)

// Readyz handles GET /readyz (FR-6): 200 when Redis answers PING, else 503.
// Served by the API so the smoke test can validate X-Instance-Id here.
func (h *Handler) Readyz(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.Store.Ping(ctx); err != nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	return c.Status(fiber.StatusOK).SendString("ok")
}

// Healthz handles GET /healthz (FR-5). In production nginx short-circuits this
// route, but the API answers it too for direct/local probes.
func (h *Handler) Healthz(c *fiber.Ctx) error {
	return c.Status(fiber.StatusOK).SendString("ok")
}

// MetricsHandler handles GET /metrics (FR-7): Prometheus text exposition.
func (h *Handler) MetricsHandler() fiber.Handler {
	return adaptor.HTTPHandler(h.Metrics.Handler())
}
