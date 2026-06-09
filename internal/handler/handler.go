package handler

import (
	"time"

	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/batch"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/metrics"
	"github.com/LuisFelipeMoro/the_500mb_club_go/internal/storage"
	"go.uber.org/zap"
)

// Handler bundles the dependencies shared by every route.
type Handler struct {
	Store   storage.Store
	Writer  *batch.Writer
	Metrics *metrics.Metrics
	Log     *zap.Logger
	// ReadTimeout bounds each read's Redis round-trip so a slow store fails fast
	// (503) instead of letting the request queue to the server WriteTimeout.
	ReadTimeout time.Duration
}

// New constructs a Handler. readTimeout bounds Redis reads (Range/anomaly).
func New(store storage.Store, writer *batch.Writer, m *metrics.Metrics, log *zap.Logger, readTimeout time.Duration) *Handler {
	return &Handler{Store: store, Writer: writer, Metrics: m, Log: log, ReadTimeout: readTimeout}
}
