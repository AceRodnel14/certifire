package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"certifire/internal/store"
	"certifire/internal/webhook"
)

type webhookView struct {
	Masked       string     `json:"masked"`
	Broken       bool       `json:"broken"`
	BrokenReason *string    `json:"brokenReason"`
	BrokenAt     *time.Time `json:"brokenAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

func viewOf(w *store.Webhook) *webhookView {
	return &webhookView{Masked: webhook.Mask(w.URL), Broken: w.BrokenAt != nil, BrokenReason: w.BrokenReason, BrokenAt: w.BrokenAt, UpdatedAt: w.UpdatedAt}
}

func pathKind(w http.ResponseWriter, r *http.Request) (webhook.Kind, bool) {
	k, ok := webhook.ParseKind(r.PathValue("kind"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown webhook kind.")
	}
	return k, ok
}

func (s *Server) handleWebhooksGet(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	ws, err := st.GetWebhooks(r.Context(), sess.UserID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := map[string]*webhookView{"discord": nil, "teams": nil}
	for k, v := range ws {
		out[k] = viewOf(v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleWebhookPut(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	kind, ok := pathKind(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	norm, err := webhook.Validate(kind, req.URL)
	var ve *webhook.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Msg)
		return
	}
	saved, err := st.SetWebhook(r.Context(), sess.UserID, string(kind), norm)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(saved))
}

func (s *Server) handleWebhookDelete(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	kind, ok := pathKind(w, r)
	if !ok {
		return
	}
	if err := st.DeleteWebhook(r.Context(), sess.UserID, string(kind)); err != nil && !errors.Is(err, store.ErrNotFound) {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebhookTest(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	kind, ok := pathKind(w, r)
	if !ok {
		return
	}
	wh, err := st.GetWebhook(r.Context(), sess.UserID, string(kind))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "No webhook configured.")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	s.testMu.Lock()
	last, seen := s.lastTest[sess.UserID]
	if seen && time.Since(last) < testInterval {
		s.testMu.Unlock()
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "Wait 10 seconds between tests.")
		return
	}
	s.lastTest[sess.UserID] = time.Now()
	s.testMu.Unlock()

	res := s.sender.Send(r.Context(), kind, wh.URL, webhook.Message{
		Title: "CertiFire test message",
		Text:  "Reminders for " + sess.Username + " will arrive here.",
	})
	switch {
	case res.Outcome == webhook.Broken:
		if err := st.MarkWebhookBroken(r.Context(), sess.UserID, string(kind), res.Message); err != nil {
			serverError(w, r, err)
			return
		}
	case res.OK():
		if err := st.ClearWebhookBroken(r.Context(), sess.UserID, string(kind)); err != nil {
			serverError(w, r, err)
			return
		}
	}
	slog.Info("webhook test", "kind", kind, "target", webhook.Redact(wh.URL), "outcome", res.Outcome)
	out := map[string]any{"outcome": res.Outcome, "message": res.Message}
	if res.Outcome == webhook.RateLimited && res.RetryAfter > 0 {
		out["retryAfter"] = int(res.RetryAfter.Seconds() + 0.999)
	}
	writeJSON(w, http.StatusOK, out)
}
