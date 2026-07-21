package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/mirmik/migi/server/internal/events"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/qlog"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migi server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", ":8443", "UDP address for the HTTP/3 server")
	ingestListen := flag.String("ingest-listen", "127.0.0.1:8787", "trusted local TCP address for event submission")
	databasePath := flag.String("db", "migi.db", "SQLite event journal path")
	cert := flag.String("cert", "", "TLS certificate chain in PEM format")
	key := flag.String("key", "", "TLS private key in PEM format")
	flag.Parse()

	if *cert == "" || *key == "" {
		return errors.New("-cert and -key are required")
	}

	journal, err := events.OpenSQLite(*databasePath)
	if err != nil {
		return err
	}
	broker := events.NewBroker(journal)
	defer broker.Close()

	publicMux := newPublicMux(broker)
	quicConfig := &quic.Config{}
	if os.Getenv("QLOGDIR") != "" {
		quicConfig.Tracer = qlog.DefaultConnectionTracer
	}
	server := http3.Server{
		Addr:           *listen,
		Handler:        publicMux,
		QUICConfig:     quicConfig,
		IdleTimeout:    90 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	ingestServer := http.Server{
		Addr:              *ingestListen,
		Handler:           newIngestMux(broker),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	serverErrors := make(chan error, 2)
	go func() {
		slog.Info("starting trusted local ingest", "address", *ingestListen)
		serverErrors <- ingestServer.ListenAndServe()
	}()
	go func() {
		slog.Info("starting public HTTP/3 server", "address", *listen)
		serverErrors <- server.ListenAndServeTLS(*cert, *key)
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var serveErr error
	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case serveErr = <-serverErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ingestErr := ingestServer.Shutdown(shutdownContext)
	quicErr := server.Shutdown(shutdownContext)
	return errors.Join(serveErr, ingestErr, quicErr)
}

func newPublicMux(broker *events.Broker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(broker))
	mux.HandleFunc("GET /v1/events", streamHandler(broker))
	mux.HandleFunc("POST /v1/ack", acknowledgeHandler(broker))
	return mux
}

func newIngestMux(broker *events.Broker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(broker))
	mux.HandleFunc("POST /v1/events", publishHandler(broker))
	return mux
}

func publishHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()

		var input events.Input
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if input.Kind == "" || input.Title == "" {
			http.Error(w, "kind and title are required", http.StatusBadRequest)
			return
		}

		event, err := broker.Publish(r.Context(), input)
		if err != nil {
			slog.Error("failed to persist event", "error", err)
			http.Error(w, "failed to persist event", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, event)
	}
}

func healthHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := broker.Healthy(r.Context()); err != nil {
			http.Error(w, "event journal is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func acknowledgeHandler(broker *events.Broker) http.HandlerFunc {
	type acknowledgement struct {
		DeviceID string `json:"device_id"`
		Through  uint64 `json:"through"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		var ack acknowledgement
		if err := decoder.Decode(&ack); err != nil || !deviceIDPattern.MatchString(ack.DeviceID) {
			http.Error(w, "valid device_id and through are required", http.StatusBadRequest)
			return
		}
		if err := broker.Acknowledge(r.Context(), ack.DeviceID, ack.Through); err != nil {
			slog.Error("failed to persist acknowledgement", "error", err)
			http.Error(w, "failed to persist acknowledgement", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func streamHandler(broker *events.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 3 {
			http.Error(w, "HTTP/3 is required", http.StatusHTTPVersionNotSupported)
			return
		}

		after, err := parseCursor(r.URL.Query().Get("after"))
		if err != nil {
			http.Error(w, "after must be an unsigned integer", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
			return
		}

		replay, stream, err := broker.Subscribe(r.Context(), after)
		if err != nil {
			slog.Error("failed to replay events", "error", err)
			http.Error(w, "event journal is unavailable", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for _, event := range replay {
			if err := writeLine(w, flusher, event); err != nil {
				return
			}
		}
		heartbeat := time.NewTicker(30 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case event, open := <-stream:
				if !open || writeLine(w, flusher, event) != nil {
					return
				}
			case now := <-heartbeat.C:
				if writeLine(w, flusher, map[string]any{
					"type": "heartbeat",
					"time": now.UTC(),
				}) != nil {
					return
				}
			}
		}
	}
}

func parseCursor(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func writeLine(w http.ResponseWriter, flusher http.Flusher, value any) error {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		slog.Warn("failed to write response", "error", err)
	}
}
