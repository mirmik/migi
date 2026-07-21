package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mirmik/migi/server/internal/events"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed templates/*.html assets/*
var content embed.FS

type Config struct {
	Broker                 *events.Broker
	PublicEndpoint         string
	CertificateFingerprint string
	PublicListen           string
	IngestListen           string
	AdminListen            string
	StartedAt              time.Time
}

type Handler struct {
	config    Config
	csrfToken string
	template  *template.Template
	assets    http.Handler
	now       func() time.Time
}

type pageData struct {
	CSRFToken              string
	PublicEndpoint         string
	CertificateFingerprint string
	PublicListen           string
	IngestListen           string
	AdminListen            string
	StartedAt              time.Time
	Uptime                 time.Duration
	Stats                  events.ServerStats
	Devices                []events.DeviceInfo
	ActiveStreams          int
	Pairing                *pairingView
	Notice                 string
}

type pairingView struct {
	QRDataURI template.URL
	ExpiresAt time.Time
}

func New(config Config) (*Handler, error) {
	if config.Broker == nil {
		return nil, errors.New("admin broker is required")
	}
	if config.StartedAt.IsZero() {
		config.StartedAt = time.Now()
	}
	if config.CertificateFingerprint == "" {
		return nil, errors.New("certificate fingerprint is required")
	}
	if config.PublicEndpoint != "" {
		parsed, err := parsePublicEndpoint(config.PublicEndpoint)
		if err != nil {
			return nil, err
		}
		config.PublicEndpoint = parsed.String()
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate admin CSRF token: %w", err)
	}
	templates, err := template.New("dashboard.html").Funcs(template.FuncMap{
		"formatTime":     func(value time.Time) string { return value.Local().Format("2006-01-02 15:04:05 MST") },
		"formatDuration": formatDuration,
	}).ParseFS(content, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}
	assetsFS, err := fs.Sub(content, "assets")
	if err != nil {
		return nil, fmt.Errorf("open admin assets: %w", err)
	}
	return &Handler{
		config:    config,
		csrfToken: base64.RawURLEncoding.EncodeToString(tokenBytes),
		template:  templates,
		assets:    http.FileServer(http.FS(assetsFS)),
		now:       time.Now,
	}, nil
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /admin/", h.dashboard)
	mux.HandleFunc("POST /admin/pair", h.createPairing)
	mux.HandleFunc("POST /admin/notifications/test", h.sendTestNotification)
	mux.HandleFunc("POST /admin/devices/revoke", h.revokeDevice)
	mux.Handle("GET /admin/assets/", http.StripPrefix("/admin/assets/", h.assets))
	return h.securityHeaders(mux)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	h.renderDashboard(w, r, nil, r.URL.Query().Get("notice"), http.StatusOK)
}

func (h *Handler) sendTestNotification(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	event, err := h.config.Broker.Publish(r.Context(), events.Input{
		Kind:  "agent.attention",
		Agent: "migi-admin",
		Title: "Migi test notification",
		Body:  "Sent from the server administration panel.",
	})
	if err != nil {
		http.Error(w, "failed to send test notification", http.StatusInternalServerError)
		return
	}
	slog.Info("test notification sent", "event_id", event.ID, "remote_addr", r.RemoteAddr)
	http.Redirect(w, r, "/admin/?notice=Test+notification+sent", http.StatusSeeOther)
}

func (h *Handler) createPairing(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	if h.config.PublicEndpoint == "" {
		http.Error(w, "pairing is unavailable until -public-endpoint is configured", http.StatusConflict)
		return
	}
	ttl, err := time.ParseDuration(r.FormValue("ttl"))
	if err != nil || ttl < time.Minute || ttl > time.Hour {
		http.Error(w, "pairing TTL must be between 1m and 1h", http.StatusBadRequest)
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		http.Error(w, "failed to generate pairing invitation", http.StatusInternalServerError)
		return
	}
	secretHash := sha256.Sum256(secret)
	expiresAt := h.now().UTC().Add(ttl)
	if err := h.config.Broker.CreatePairingCode(r.Context(), secretHash[:], expiresAt); err != nil {
		http.Error(w, "failed to persist pairing invitation", http.StatusInternalServerError)
		return
	}
	slog.Info("pairing invitation created",
		"expires_at", expiresAt,
		"public_endpoint", h.config.PublicEndpoint,
		"remote_addr", r.RemoteAddr,
	)
	invitation := &url.URL{Scheme: "migi", Host: "pair"}
	query := invitation.Query()
	query.Set("endpoint", h.config.PublicEndpoint)
	query.Set("pin", strings.ReplaceAll(h.config.CertificateFingerprint, ":", ""))
	query.Set("secret", base64.RawURLEncoding.EncodeToString(secret))
	query.Set("expires", expiresAt.Format(time.RFC3339))
	invitation.RawQuery = query.Encode()
	png, err := qrcode.Encode(invitation.String(), qrcode.Medium, 384)
	if err != nil {
		http.Error(w, "failed to render pairing QR", http.StatusInternalServerError)
		return
	}
	h.renderDashboard(w, r, &pairingView{
		QRDataURI: template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png)),
		ExpiresAt: expiresAt,
	}, "Pairing invitation created", http.StatusCreated)
}

func (h *Handler) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if !h.validForm(w, r) {
		return
	}
	deviceID := r.FormValue("device_id")
	if deviceID == "" {
		http.Error(w, "device_id is required", http.StatusBadRequest)
		return
	}
	if err := h.config.Broker.RevokeDevice(r.Context(), deviceID); err != nil {
		if errors.Is(err, events.ErrUnauthorized) {
			http.Error(w, "device is unknown or already revoked", http.StatusConflict)
			return
		}
		http.Error(w, "failed to revoke device", http.StatusInternalServerError)
		return
	}
	slog.Info("device revoked", "device_id", deviceID, "remote_addr", r.RemoteAddr)
	http.Redirect(w, r, "/admin/?notice=Device+revoked", http.StatusSeeOther)
}

func (h *Handler) renderDashboard(
	w http.ResponseWriter,
	r *http.Request,
	pairing *pairingView,
	notice string,
	status int,
) {
	stats, err := h.config.Broker.Stats(r.Context())
	if err != nil {
		http.Error(w, "failed to read server statistics", http.StatusInternalServerError)
		return
	}
	devices, err := h.config.Broker.ListDevices(r.Context())
	if err != nil {
		http.Error(w, "failed to read paired devices", http.StatusInternalServerError)
		return
	}
	now := h.now()
	data := pageData{
		CSRFToken:              h.csrfToken,
		PublicEndpoint:         h.config.PublicEndpoint,
		CertificateFingerprint: h.config.CertificateFingerprint,
		PublicListen:           h.config.PublicListen,
		IngestListen:           h.config.IngestListen,
		AdminListen:            h.config.AdminListen,
		StartedAt:              h.config.StartedAt,
		Uptime:                 now.Sub(h.config.StartedAt),
		Stats:                  stats,
		Devices:                devices,
		ActiveStreams:          h.config.Broker.SubscriberCount(),
		Pairing:                pairing,
		Notice:                 notice,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.template.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		return
	}
}

func (h *Handler) validForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	provided := r.FormValue("csrf_token")
	if len(provided) != len(h.csrfToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.csrfToken)) != 1 {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

func (h *Handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func parsePublicEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, errors.New("public endpoint must be a plain https://host[:port] URL")
	}
	return parsed, nil
}

func NormalizeFingerprint(raw []byte) string {
	encoded := strings.ToUpper(hex.EncodeToString(raw))
	return strings.Join(chunk(encoded, 2), ":")
}

func chunk(value string, size int) []string {
	result := make([]string, 0, len(value)/size)
	for len(value) > 0 {
		length := min(size, len(value))
		result = append(result, value[:length])
		value = value[length:]
	}
	return result
}

func formatDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return value.Round(time.Second).String()
}
