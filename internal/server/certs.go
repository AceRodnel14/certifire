package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"certifire/internal/certgen"
	"certifire/internal/crypto"
	"certifire/internal/store"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type certMeta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hosts     []string  `json:"hosts"`
	Bits      int       `json:"bits"`
	Days      int       `json:"days"`
	CreatedAt time.Time `json:"createdAt"`
	NotAfter  time.Time `json:"notAfter"`
	DaysLeft  int       `json:"daysLeft"`
	RenewedBy *string   `json:"renewedBy"`
}

type certDetail struct {
	certMeta
	Subject   certgen.Subject `json:"subject"`
	NotBefore time.Time       `json:"notBefore"`
	Crt       string          `json:"crt"`
	Key       string          `json:"key"`
	Cnf       string          `json:"cnf"`
}

func metaOf(c *store.Cert) certMeta {
	return certMeta{ID: c.ID, Name: c.Name, Hosts: c.Hosts, Bits: c.Bits, Days: c.Days, CreatedAt: c.CreatedAt,
		NotAfter: c.NotAfter, DaysLeft: certgen.DaysLeft(c.NotAfter, time.Now()), RenewedBy: c.RenewedBy}
}

func detailOf(c *store.Cert, key string) certDetail {
	var subj certgen.Subject
	_ = json.Unmarshal(c.Subject, &subj)
	return certDetail{certMeta: metaOf(c), Subject: subj, NotBefore: c.NotBefore, Crt: c.CertPEM, Key: key, Cnf: c.CNF}
}

// pathCert loads the user's cert from the {id} path value (404 for anything else).
func pathCert(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) *store.Cert {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "Certificate not found.")
		return nil
	}
	c, err := st.GetCert(r.Context(), sess.UserID, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Certificate not found.")
		return nil
	} else if err != nil {
		serverError(w, r, err)
		return nil
	}
	return c
}

func (s *Server) handleCertList(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	certs, err := st.ListCerts(r.Context(), sess.UserID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := make([]certMeta, len(certs))
	for i, c := range certs {
		out[i] = metaOf(c)
	}
	writeJSON(w, http.StatusOK, out)
}

type createReq struct {
	Name    string          `json:"name"`
	Hosts   []string        `json:"hosts"`
	Bits    int             `json:"bits"`
	Days    int             `json:"days"`
	Subject certgen.Subject `json:"subject"`
	RenewOf string          `json:"renewOf"`
}

func (s *Server) handleCertCreate(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	var req createReq
	if !readJSON(w, r, &req) {
		return
	}
	s.createCert(w, r, st, sess, req)
}

func (s *Server) handleCertRenew(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	old := pathCert(w, r, st, sess)
	if old == nil {
		return
	}
	var subj certgen.Subject
	if err := json.Unmarshal(old.Subject, &subj); err != nil {
		serverError(w, r, err)
		return
	}
	s.createCert(w, r, st, sess, createReq{Name: old.Name, Hosts: old.Hosts, Bits: old.Bits, Days: old.Days, Subject: subj, RenewOf: old.ID})
}

// createCert validates, generates, encrypts and stores a cert; with RenewOf it
// also marks the old cert renewed in the same transaction.
func (s *Server) createCert(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session, req createReq) {
	if req.Bits == 0 {
		req.Bits = 4096
	}
	if req.Days == 0 {
		req.Days = certgen.MaxDays
	}
	spec, err := certgen.Validate(certgen.Request{Name: req.Name, Hosts: req.Hosts, Bits: req.Bits, Days: req.Days, Subject: req.Subject})
	var ve *certgen.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Msg)
		return
	}
	if req.RenewOf != "" {
		// Fail fast before the slow generation; InsertCert re-checks in its transaction.
		if !uuidRe.MatchString(req.RenewOf) {
			writeError(w, http.StatusNotFound, "Certificate to renew not found.")
			return
		}
		old, err := st.GetCert(r.Context(), sess.UserID, req.RenewOf)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Certificate to renew not found.")
			return
		} else if err != nil {
			serverError(w, r, err)
			return
		}
		if old.RenewedBy != nil {
			writeError(w, http.StatusConflict, "That certificate was already renewed.")
			return
		}
	}
	res, err := s.gen.Generate(r.Context(), spec)
	if errors.Is(err, certgen.ErrBusy) {
		writeError(w, http.StatusTooManyRequests, "Busy generating certificates, retry in a moment.")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	subj, _ := json.Marshal(spec.Subject)
	c := &store.Cert{
		ID: crypto.NewUUID(), UserID: sess.UserID, Name: spec.Name, Hosts: spec.HostValues(), Bits: spec.Bits, Days: spec.Days,
		Subject: subj, CNF: res.CNF, CertPEM: res.CertPEM,
		NotBefore: res.NotBefore, NotAfter: res.NotAfter,
	}
	c.KeyEnc = crypto.Seal(sess.DEK, []byte(res.KeyPEM), crypto.CertKeyAAD(sess.UserID, c.ID))
	switch err := st.InsertCert(r.Context(), c, req.RenewOf); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Certificate to renew not found.")
		return
	case errors.Is(err, store.ErrAlreadyRenewed):
		writeError(w, http.StatusConflict, "That certificate was already renewed.")
		return
	case err != nil:
		serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, detailOf(c, res.KeyPEM))
}

func decryptKey(sess *Session, c *store.Cert) (string, error) {
	key, err := crypto.Open(sess.DEK, c.KeyEnc, crypto.CertKeyAAD(sess.UserID, c.ID))
	return string(key), err
}

func (s *Server) handleCertGet(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	c := pathCert(w, r, st, sess)
	if c == nil {
		return
	}
	key, err := decryptKey(sess, c)
	if err != nil {
		serverError(w, r, errors.New("decrypt private key: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, detailOf(c, key))
}

func (s *Server) handleCertFile(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	var ext, ctype string
	switch r.PathValue("file") {
	case "crt":
		ext, ctype = "crt", "application/x-pem-file"
	case "key":
		ext, ctype = "key", "application/x-pem-file"
	case "cnf":
		ext, ctype = "cnf", "text/plain; charset=utf-8"
	default:
		writeError(w, http.StatusNotFound, "Unknown file.")
		return
	}
	c := pathCert(w, r, st, sess)
	if c == nil {
		return
	}
	var body string
	switch ext {
	case "crt":
		body = c.CertPEM
	case "cnf":
		body = c.CNF
	default:
		key, err := decryptKey(sess, c)
		if err != nil {
			serverError(w, r, errors.New("decrypt private key: "+err.Error()))
			return
		}
		body = key
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", `attachment; filename="`+c.Name+"."+ext+`"`) // name is validated [A-Za-z0-9._-]
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleCertDelete(w http.ResponseWriter, r *http.Request, st *store.Store, sess *Session) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "Certificate not found.")
		return
	}
	if err := st.DeleteCert(r.Context(), sess.UserID, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Certificate not found.")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
