package admin

import (
	"log/slog"
	"net/http"
)

func (h *Handler) musicPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.template.ExecuteTemplate(w, "music.html", h.config.Music != nil); err != nil {
		slog.Error("failed to render music player", "error", err)
	}
}
