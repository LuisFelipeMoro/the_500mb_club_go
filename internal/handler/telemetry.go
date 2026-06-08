package handler

import (
	"context"
	"encoding/json"
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
	if err := json.Unmarshal(c.Body(), &p); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if err := validate.Point(p); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	h.Writer.Push(id, [][]byte{p.Encode()})
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
	var body struct {
		Points []model.TelemetryPoint `json:"points"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if len(body.Points) > 100 {
		return c.SendStatus(fiber.StatusRequestEntityTooLarge)
	}
	if len(body.Points) == 0 {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	if err := validate.Points(body.Points); err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	encoded := make([][]byte, len(body.Points))
	for i := range body.Points {
		encoded[i] = body.Points[i].Encode()
	}
	if h.Writer.Push(id, encoded) {
		return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"accepted": len(encoded)})
	}
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

	members, err := h.Store.Range(context.Background(), id, fromTS, to, offset, int64(limit+1))
	if err != nil {
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
