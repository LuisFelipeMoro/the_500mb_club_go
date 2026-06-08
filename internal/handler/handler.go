package handler

import (
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
}

// New constructs a Handler.
func New(store storage.Store, writer *batch.Writer, m *metrics.Metrics, log *zap.Logger) *Handler {
	return &Handler{Store: store, Writer: writer, Metrics: m, Log: log}
}
