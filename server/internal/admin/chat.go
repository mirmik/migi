package admin

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
)

func (h *Handler) chatPage(w http.ResponseWriter, r *http.Request) {
	devices, err := h.config.Broker.ListDevices(r.Context())
	if err != nil {
		http.Error(w, "Не удалось загрузить устройства", 500)
		return
	}
	data := struct {
		CSRFToken string
		Devices   any
		Enabled   bool
	}{h.csrfToken, devices, h.config.AgentRequest != nil}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.template.ExecuteTemplate(w, "chat.html", data)
}

func (h *Handler) chatJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !h.validCSRF(r.Header.Get("X-CSRF-Token")) {
		http.Error(w, "Обновите страницу: недействительный CSRF token", 403)
		return false
	}
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		http.Error(w, "Ожидается JSON", 415)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "Некорректный запрос", 400)
		return false
	}
	return true
}

func (h *Handler) agentProxy(w http.ResponseWriter, r *http.Request, path string, payload any) {
	if h.config.AgentRequest == nil {
		http.Error(w, "Агент не настроен", 503)
		return
	}
	var output json.RawMessage
	code, err := h.config.AgentRequest(r.Context(), r.Method, path, payload, &output)
	if err != nil {
		if code >= 400 && code < 500 {
			http.Error(w, "Действие отклонено. Обновите состояние; возможно, агент занят или данные уже изменились.", code)
		} else {
			http.Error(w, "Агент временно недоступен", 503)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(output)
}

func (h *Handler) chatAPI(w http.ResponseWriter, r *http.Request) {
	devices, err := h.config.Broker.ListDevices(r.Context())
	if err != nil {
		http.Error(w, "Не удалось загрузить устройства", 500)
		return
	}
	id := r.URL.Query().Get("device")
	found := false
	for _, d := range devices {
		if d.ID == id && d.RevokedAt == nil {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "Выберите подключённое к Migi устройство", 404)
		return
	}
	owner := "device:" + id
	if r.Method == http.MethodGet {
		limit := 40
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 10000 {
				http.Error(w, "Некорректный размер истории", 400)
				return
			}
			limit = n
		}
		h.agentProxy(w, r, "/migi/chat?owner="+url.QueryEscape(owner)+"&limit="+strconv.Itoa(limit), nil)
		return
	}
	var input struct {
		Action       string `json:"action"`
		RequestID    string `json:"request_id"`
		ThreadID     string `json:"thread_id"`
		TargetThread string `json:"target_thread,omitempty"`
		Text         string `json:"text,omitempty"`
	}
	if !h.chatJSON(w, r, &input) {
		return
	}
	switch input.Action {
	case "send", "stop", "new", "select", "compact":
	default:
		http.Error(w, "Неизвестное действие", 400)
		return
	}
	if len(input.Text) > 64<<10 {
		http.Error(w, "Сообщение слишком длинное", 413)
		return
	}
	h.agentProxy(w, r, "/migi/chat", map[string]string{"owner": owner, "action": input.Action, "request_id": input.RequestID, "thread_id": input.ThreadID, "target_thread": input.TargetThread, "text": input.Text})
}

func (h *Handler) promptAPI(w http.ResponseWriter, r *http.Request) {
	var payload any
	if r.Method == http.MethodPost {
		var input struct {
			Text     string `json:"text"`
			Revision string `json:"revision"`
			Reset    bool   `json:"reset"`
		}
		if !h.chatJSON(w, r, &input) {
			return
		}
		payload = input
	}
	h.agentProxy(w, r, "/migi/prompt", payload)
}
