package notebook

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// Actor is supplied only by the embedding server's authenticated middleware.
type Actor struct {
	ID    string
	Owner bool
}
type ActorFunc func(*http.Request) Actor

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	status := http.StatusBadRequest
	if !errors.As(err, &e) {
		slog.Error("notebook request failed", "error", err)
		e = &Error{Code: "internal_error", Message: "notebook storage unavailable"}
		status = 500
	} else {
		switch e.Code {
		case "not_found":
			status = 404
		case "forbidden":
			status = 403
		case "key_exists", "revision_conflict", "operation_conflict", "deleted", "stale_cursor", "import_conflict":
			status = 409
		}
	}
	JSON(w, status, map[string]any{"error": e})
}
func Decode(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		JSON(w, 415, map[string]any{"error": &Error{Code: "invalid_content_type", Message: "application/json required"}})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil {
		err = decoder.Decode(&struct{}{})
		if errors.Is(err, io.EOF) {
			return true
		}
	}
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		JSON(w, 413, map[string]any{"error": &Error{Code: "too_large", Message: "request exceeds size limit"}})
	} else {
		WriteError(w, invalid("expected one JSON object with known fields"))
	}
	return false
}

// Handler serves POST operations mounted at a route with {operation}.
// Export/import are owner-only. They never expose access grants or tokens.
func (s *Store) Handler(actor ActorFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(405)
			return
		}
		a := actor(r)
		op := r.PathValue("operation")
		write := op == "create" || op == "update" || op == "delete" || op == "restore"
		if a.ID == "" {
			WriteError(w, &Error{Code: "forbidden", Message: "authenticated identity required"})
			return
		}
		if !a.Owner {
			p, err := s.Permission(r.Context(), a.ID)
			if err != nil {
				WriteError(w, err)
				return
			}
			if !p.Read || (write && !p.Write) || op == "export" || op == "import" {
				WriteError(w, &Error{Code: "forbidden", Message: "notebook permission required"})
				return
			}
		}
		var result any
		var err error
		switch op {
		case "search", "list":
			var in Search
			if !Decode(w, r, &in, 16<<10) {
				return
			}
			if op == "search" && in.Query == "" {
				WriteError(w, invalid("search requires query; use list to browse"))
				return
			}
			if op == "list" && in.Query != "" {
				WriteError(w, invalid("list does not accept query"))
				return
			}
			result, err = s.Search(r.Context(), in)
		case "read":
			var in struct {
				Keys []string `json:"keys"`
			}
			if !Decode(w, r, &in, 16<<10) {
				return
			}
			var records []Record
			records, err = s.Read(r.Context(), in.Keys)
			result = map[string]any{"items": records}
		case "history":
			var in struct {
				Key      string `json:"key"`
				Revision int64  `json:"revision,omitempty"`
			}
			if !Decode(w, r, &in, 16<<10) {
				return
			}
			result, err = s.History(r.Context(), in.Key, in.Revision)
		case "create", "update", "delete", "restore":
			var in Mutation
			if !Decode(w, r, &in, 512<<10) {
				return
			}
			result, err = s.Mutate(r.Context(), a.ID, op, in)
		case "export":
			var in struct{}
			if !Decode(w, r, &in, 1024) {
				return
			}
			result, err = s.Export(r.Context())
			w.Header().Set("Content-Disposition", `attachment; filename="notebook.json"`)
		case "import":
			var in Export
			if !Decode(w, r, &in, 64<<20) {
				return
			}
			err = s.Import(r.Context(), in)
			result = map[string]bool{"restored": err == nil}
		default:
			err = &Error{Code: "not_found", Message: "unknown notebook operation"}
		}
		if err != nil {
			WriteError(w, err)
			return
		}
		JSON(w, 200, result)
	})
}
