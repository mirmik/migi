package admin

import (
	"log/slog"
	"net/http"
)

func (h *Handler) musicPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.template.ExecuteTemplate(w, "music.html", h.config.Media != nil); err != nil {
		slog.Error("failed to render music player", "error", err)
	}
}

func (h *Handler) videoPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		Enabled   bool
		CSRFToken string
	}{h.config.Media != nil, h.csrfToken}
	if err := h.template.ExecuteTemplate(w, "video.html", data); err != nil {
		slog.Error("failed to render video player", "error", err)
	}
}
