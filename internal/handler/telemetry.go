package handler

import (
	"context"
	"errors"
	"strconv"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/model"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/storage"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/validate"
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// PostTelemetry handles POST /devices/{id}/telemetry (FR-1).
// Validate → encode → non-blocking channel push → 202 empty. Never hits Redis.
func (h *Handler) PostTelemetry(c *fiber.Ctx) error {
	c.Locals("op", "post")
	id := c.Params("id")
	if !validate.DeviceID(id) {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	var p model.TelemetryPoint
	if err := model.UnmarshalPoint(c.Body(), &p); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if err := validate.Point(p); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if !h.Writer.Push(id, [][]byte{p.Encode()}) {
		h.Metrics.WritesDropped.Inc() // buffer full: accepted-and-dropped, still 202 (FR-1)
	}
	return c.SendStatus(fiber.StatusAccepted)
}

// PostBatch handles POST /devices/{id}/telemetry/batch (FR-2).
// Check order: 413 (>100) → 400 (empty) → 400 (any invalid) → encode-all →
// single channel push. Returns {"accepted": N} or {"accepted": 0} on overflow.
func (h *Handler) PostBatch(c *fiber.Ctx) error {
	c.Locals("op", "batch")
	id := c.Params("id")
	if !validate.DeviceID(id) {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	var points []model.TelemetryPoint
	if err := model.UnmarshalBatch(c.Body(), &points); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if len(points) > 100 {
		return c.SendStatus(fiber.StatusRequestEntityTooLarge)
	}
	if len(points) == 0 {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if err := validate.Points(points); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	encoded := make([][]byte, len(points))
	for i := range points {
		encoded[i] = points[i].Encode()
	}
	if h.Writer.Push(id, encoded) {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"accepted": len(encoded)})
	}
	h.Metrics.WritesDropped.Add(float64(len(encoded))) // buffer full: report accepted:0
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"accepted": 0})
}

type rangeResponse struct {
	Points     []model.TelemetryPoint `json:"points"`
	NextCursor *string                `json:"next_cursor"`
}

// GetTelemetry handles GET /devices/{id}/telemetry (FR-3): time-window range
// with tie-safe cursor pagination. One Redis round-trip.
func (h *Handler) GetTelemetry(c *fiber.Ctx) error {
	c.Locals("op", "range")
	id := c.Params("id")
	if !validate.DeviceID(id) {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	fromStr, toStr := c.Query("from"), c.Query("to")
	if fromStr == "" || toStr == "" {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	from, err := strconv.ParseInt(fromStr, 10, 64)
	if err != nil || from < 0 {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	to, err := strconv.ParseInt(toStr, 10, 64)
	if err != nil || from > to {
		return c.SendStatus(fiber.StatusBadRequest)
	}

	limit := 100
	if ls := c.Query("limit"); ls != "" {
		limit, err = strconv.Atoi(ls)
		if err != nil || limit < 1 || limit > 500 {
			return c.SendStatus(fiber.StatusBadRequest)
		}
	}

	fromTS, offset := from, int64(0)
	var prev *storage.Cursor
	if cs := c.Query("cursor"); cs != "" {
		cur, err := storage.DecodeCursor(cs)
		if err != nil {
			return c.SendStatus(fiber.StatusBadRequest)
		}
		prev, fromTS, offset = &cur, cur.TS, cur.Offset
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), h.ReadTimeout)
	defer cancel()
	members, err := h.Store.Range(ctx, id, fromTS, to, offset, int64(limit+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			h.Metrics.ReadTimeouts.Inc()
		}
		h.Log.Error("range query failed", zap.Error(err))
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}

	tsList := make([]int64, len(members))
	points := make([]model.TelemetryPoint, len(members))
	for i, m := range members {
		points[i] = model.DecodePoint(m)
		tsList[i] = points[i].Ts
	}

	next := storage.NextCursor(tsList, limit, prev)
	pageLen := len(points)
	if pageLen > limit {
		pageLen = limit
	}

	resp := rangeResponse{Points: points[:pageLen]}
	if next != nil {
		enc := storage.EncodeCursor(*next)
		resp.NextCursor = &enc
	}
	return c.Status(fiber.StatusOK).JSON(resp)
}
