package swiss

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/phturb/bonjack-tools-backend-go/internal/logger"
)

// maxBodyBytes bounds JSON request bodies; a participant list is tiny.
const maxBodyBytes = 1 << 20

// devSubject is the owner used when JWT is not configured (local development)
// and the request carries no bearer token.
const devSubject = "dev"

// devCallerSubject returns the caller identity in development mode (no JWT):
// the raw value of `Authorization: Bearer <subject>` so a frontend can act as
// any identity (owner vs. non-owner), or devSubject when the header is absent.
func devCallerSubject(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		if sub := strings.TrimSpace(h[len(prefix):]); sub != "" {
			return sub
		}
	}
	return devSubject
}

// SubjectFunc returns the JWT subject stored in the request context.
type SubjectFunc func(ctx context.Context) (string, bool)

// RegisterRoutes mounts the Swiss endpoints under r (the /api subrouter).
// auth wraps every mutation when non-nil; when nil the subject is the bearer
// token value as-is, or "dev" without one (see devCallerSubject).
func RegisterRoutes(r *mux.Router, auth func(http.Handler) http.Handler, subject SubjectFunc, svc *Service) {
	h := &handlers{svc: svc, auth: auth, subject: subject, log: logger.ComponentLogger("swiss-api")}

	r.HandleFunc("/swiss", h.list).Methods(http.MethodGet)
	r.HandleFunc("/swiss/{id}", h.get).Methods(http.MethodGet)

	r.Handle("/swiss", h.protect(h.create)).Methods(http.MethodPost)
	r.Handle("/swiss/{id}", h.protect(h.update)).Methods(http.MethodPatch)
	r.Handle("/swiss/{id}", h.protect(h.delete)).Methods(http.MethodDelete)
	r.Handle("/swiss/{id}/participants", h.protect(h.replaceParticipants)).Methods(http.MethodPut)
	r.Handle("/swiss/{id}/participants/{pid}/drop", h.protect(h.drop)).Methods(http.MethodPost)
	r.Handle("/swiss/{id}/start", h.protect(h.start)).Methods(http.MethodPost)
	r.Handle("/swiss/{id}/matches/{mid}/result", h.protect(h.result)).Methods(http.MethodPut)
	r.Handle("/swiss/{id}/rounds/close", h.protect(h.closeRound)).Methods(http.MethodPost)
	r.Handle("/swiss/{id}/rounds/undo", h.protect(h.undoRound)).Methods(http.MethodPost)
}

type handlers struct {
	svc     *Service
	auth    func(http.Handler) http.Handler
	subject SubjectFunc
	log     *logger.Logger
}

// ownedHandler receives the caller's subject.
type ownedHandler func(w http.ResponseWriter, r *http.Request, subject string)

func (h *handlers) protect(fn ownedHandler) http.Handler {
	var inner http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := devCallerSubject(r)
		if h.auth != nil {
			var ok bool
			if sub, ok = h.subject(r.Context()); !ok || sub == "" {
				writeError(w, http.StatusUnauthorized, "missing token")
				return
			}
		}
		fn(w, r, sub)
	})
	if h.auth != nil {
		inner = h.auth(inner)
	}
	return inner
}

func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	_ = writeJSON(w, status, map[string]string{"error": msg})
}

func (h *handlers) respond(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			writeError(w, e.Status, e.Message)
			return
		}
		h.log.Error("swiss request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := writeJSON(w, status, v); err != nil {
		h.log.Error("swiss response failed", "error", err)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.List(r.Context())
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.Get(r.Context(), mux.Vars(r)["id"])
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request, sub string) {
	var in ConfigInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.svc.Create(r.Context(), sub, in)
	h.respond(w, http.StatusCreated, out, err)
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request, sub string) {
	var in ConfigInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.svc.Update(r.Context(), sub, mux.Vars(r)["id"], in)
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request, sub string) {
	if err := h.svc.Delete(r.Context(), sub, mux.Vars(r)["id"]); err != nil {
		h.respond(w, 0, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) replaceParticipants(w http.ResponseWriter, r *http.Request, sub string) {
	var in []ParticipantInput
	if !decode(w, r, &in) {
		return
	}
	out, err := h.svc.ReplaceParticipants(r.Context(), sub, mux.Vars(r)["id"], in)
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) drop(w http.ResponseWriter, r *http.Request, sub string) {
	v := mux.Vars(r)
	out, err := h.svc.Drop(r.Context(), sub, v["id"], v["pid"])
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) start(w http.ResponseWriter, r *http.Request, sub string) {
	out, err := h.svc.Start(r.Context(), sub, mux.Vars(r)["id"])
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) result(w http.ResponseWriter, r *http.Request, sub string) {
	var in ResultInput
	if !decode(w, r, &in) {
		return
	}
	v := mux.Vars(r)
	out, err := h.svc.SetResult(r.Context(), sub, v["id"], v["mid"], in)
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) closeRound(w http.ResponseWriter, r *http.Request, sub string) {
	out, err := h.svc.CloseRound(r.Context(), sub, mux.Vars(r)["id"])
	h.respond(w, http.StatusOK, out, err)
}

func (h *handlers) undoRound(w http.ResponseWriter, r *http.Request, sub string) {
	out, err := h.svc.UndoRound(r.Context(), sub, mux.Vars(r)["id"])
	h.respond(w, http.StatusOK, out, err)
}
