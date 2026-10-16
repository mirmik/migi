package admin

import (
	"net/http"

	"github.com/mirmik/migi/server/internal/notebook"
)

func (h *Handler) notebookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/notebook/{$}", h.notebookPage)
	mux.HandleFunc("POST /admin/notebook/permissions", h.notebookPermission)
	mux.Handle("POST /admin/notebook/api/{operation}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.config.Notebook == nil {
			http.Error(w, "Notebook is unavailable", 503)
			return
		}
		if !h.validCSRF(r.Header.Get("X-CSRF-Token")) {
			http.Error(w, "Refresh the page: invalid CSRF token", 403)
			return
		}
		h.config.Notebook.Handler(func(*http.Request) notebook.Actor { return notebook.Actor{ID: "owner", Owner: true} }).ServeHTTP(w, r)
	}))
}
func (h *Handler) notebookPage(w http.ResponseWriter, r *http.Request) {
	if h.config.Notebook == nil {
		http.Error(w, "Notebook is unavailable", 503)
		return
	}
	tokens, err := h.config.Broker.ListAgentTokens(r.Context())
	if err != nil {
		http.Error(w, "Unable to list agents", 500)
		return
	}
	type grant struct {
		ID    string
		Name  string
		Read  bool
		Write bool
	}
	data := struct {
		CSRFToken string
		Grants    []grant
	}{CSRFToken: h.csrfToken}
	for _, t := range tokens {
		if t.RevokedAt != nil {
			continue
		}
		p, e := h.config.Notebook.Permission(r.Context(), t.ID)
		if e != nil {
			http.Error(w, "Unable to load permissions", 500)
			return
		}
		data.Grants = append(data.Grants, grant{t.ID, t.Name, p.Read, p.Write})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.template.ExecuteTemplate(w, "notebook.html", data)
}
func (h *Handler) notebookPermission(w http.ResponseWriter, r *http.Request) {
	if h.config.Notebook == nil {
		http.Error(w, "Notebook is unavailable", 503)
		return
	}
	if !h.validForm(w, r) {
		return
	}
	tokens, err := h.config.Broker.ListAgentTokens(r.Context())
	if err != nil {
		http.Error(w, "Unable to list agents", 500)
		return
	}
	for _, t := range tokens {
		if t.ID == r.FormValue("token_id") && t.RevokedAt == nil {
			p := notebook.Permission{Read: r.FormValue("read") == "on", Write: r.FormValue("write") == "on"}
			if err = h.config.Notebook.SetPermission(r.Context(), t.ID, p); err != nil {
				notebook.WriteError(w, err)
				return
			}
			http.Redirect(w, r, "/admin/notebook/", http.StatusSeeOther)
			return
		}
	}
	http.Error(w, "Active agent token not found", 404)
}
