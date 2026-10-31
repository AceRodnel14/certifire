package server

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	yzip "github.com/yeka/zip"

	"certifire/internal/store"
)

const backupPW = "correct horse battery"

func TestLoadConfigBackupAllIPs(t *testing.T) {
	if got := testConfig(t, nil).BackupAllIPs; len(got) != 0 {
		t.Errorf("default %v", got)
	}
	cfg := testConfig(t, map[string]string{"BACKUP_ALL_ALLOWED_IPS": " 192.0.2.7 , 172.16.5.5/12,2001:db8::1, ::ffff:10.1.2.3 ,,::ffff:10.0.0.0/104"})
	var got []string
	for _, p := range cfg.BackupAllIPs {
		got = append(got, p.String())
	}
	want := []string{"192.0.2.7/32", "172.16.0.0/12", "2001:db8::1/128", "10.1.2.3/32", "10.0.0.0/8"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"nope", "10.0.0.0/99", "1.2.3.4,x"} {
		_, err := LoadConfig(func(k string) string {
			if k == "BACKUP_ALL_ALLOWED_IPS" {
				return bad
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "BACKUP_ALL_ALLOWED_IPS") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestInPrefixesMapped(t *testing.T) {
	list := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}
	if !inPrefixes(netip.MustParseAddr("::ffff:172.17.0.1"), list) || inPrefixes(netip.MustParseAddr("192.0.2.1"), list) {
		t.Error("mapped address matching")
	}
}

func TestFileBases(t *testing.T) {
	c := func(id, name string) *store.Cert { return &store.Cert{ID: id, Name: name} }
	tests := map[string]struct {
		in   []*store.Cert
		want []string
	}{
		"empty":         {nil, []string{}},
		"unique":        {[]*store.Cert{c("u1", "a"), c("u2", "b")}, []string{"a", "b"}},
		"renewal":       {[]*store.Cert{c("u2", "web"), c("u1", "web")}, []string{"web", "web_u1"}},
		"case":          {[]*store.Cert{c("u2", "Web"), c("u1", "web")}, []string{"Web", "web_u1"}},
		"three":         {[]*store.Cert{c("u3", "web"), c("u2", "web"), c("u1", "web")}, []string{"web", "web_u2", "web_u1"}},
		"literal last":  {[]*store.Cert{c("u2", "web"), c("u1", "web"), c("u3", "web_u1")}, []string{"web", "web_u1", "web_u1_u3"}},
		"literal first": {[]*store.Cert{c("u3", "web_u1"), c("u2", "web"), c("u1", "web")}, []string{"web_u1", "web", "web_u1_2"}},
	}
	for name, tc := range tests {
		got := fileBases(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
		seen := map[string]bool{}
		for _, g := range got {
			if seen[g] {
				t.Errorf("%s: duplicate %q", name, g)
			}
			seen[g] = true
		}
	}
}

// ba sends a request straight to the handler with the given peer address.
func (e *env) ba(method, peer string, hdr map[string]string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/api/backup-all", rd)
	req.RemoteAddr = peer
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func backupEnv(t *testing.T) (*env, string) {
	e := newEnv(t)
	e.srv.cfg.BackupAllIPs = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	return e, e.setup("adminpass1")
}

const okPeer = "192.0.2.10:5555"

func good(mk string) map[string]string {
	return map[string]string{"masterKey": mk, "backupPassword": backupPW}
}

func TestBackupAllDisabledAndDenied(t *testing.T) {
	// No database needed: these all answer before the DB check.
	h := New(testConfig(t, nil), nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/backup-all", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 404 || strings.TrimSpace(rec.Body.String()) != `{"error":"Not found."}` {
		t.Errorf("disabled: %d %s", rec.Code, rec.Body)
	}
	cfg := testConfig(t, map[string]string{"BACKUP_ALL_ALLOWED_IPS": "192.0.2.0/24"})
	h = New(cfg, nil)
	try := func(method, peer string, hdr map[string]string) int {
		req := httptest.NewRequest(method, "/api/backup-all", nil)
		req.RemoteAddr = peer
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := try("POST", okPeer, nil); c != 503 {
		t.Errorf("allowed without db: %d", c)
	}
	if c := try("POST", "198.51.100.1:1", nil); c != 404 {
		t.Errorf("not allowed: %d", c)
	}
	if c := try("POST", "garbage", nil); c != 404 {
		t.Errorf("bad peer: %d", c)
	}
	for _, m := range []string{"GET", "PUT", "DELETE", "HEAD"} {
		if c := try(m, okPeer, nil); c != 404 {
			t.Errorf("%s: %d", m, c)
		}
	}
	for _, hd := range forwardingHeaders {
		if c := try("POST", okPeer, map[string]string{hd: "192.0.2.10"}); c != 404 {
			t.Errorf("%s: %d", hd, c)
		}
	}
	if c := try("POST", "[::ffff:192.0.2.10]:1", nil); c != 503 {
		t.Errorf("v4-mapped peer: %d", c)
	}
}

func TestBackupAllValidation(t *testing.T) {
	e, mk := backupEnv(t)
	if r := e.ba("POST", okPeer, nil, nil); r.Code != 415 {
		t.Errorf("non-json: %d", r.Code)
	}
	req := httptest.NewRequest("POST", "/api/backup-all", strings.NewReader("{"))
	req.RemoteAddr = okPeer
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("bad json: %d", rec.Code)
	}
	for name, body := range map[string]map[string]string{
		"short pw":    {"masterKey": mk, "backupPassword": "123456789012345"},
		"missing key": {"backupPassword": backupPW},
		"blank key":   {"masterKey": "  ", "backupPassword": backupPW},
	} {
		r := e.ba("POST", okPeer, nil, body)
		if r.Code != 400 || !strings.Contains(r.Body.String(), "masterKey is required and backupPassword must be at least 16 characters.") {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	// Multi-byte runes count as characters, not bytes.
	r := e.ba("POST", okPeer, nil, map[string]string{"masterKey": "x", "backupPassword": strings.Repeat("é", 16)})
	if r.Code != 401 {
		t.Errorf("16 runes: %d", r.Code)
	}
}

func TestBackupAllThrottle(t *testing.T) {
	e, _ := backupEnv(t)
	bad := map[string]string{"masterKey": "wrong-master-key-0000", "backupPassword": backupPW}
	for i := 0; i < 5; i++ {
		if r := e.ba("POST", okPeer, nil, bad); r.Code != 401 || !strings.Contains(r.Body.String(), "That master key is not correct.") {
			t.Fatalf("attempt %d: %d %s", i, r.Code, r.Body)
		}
	}
	r := e.ba("POST", okPeer, nil, bad)
	if r.Code != 429 || !strings.Contains(r.Body.String(), "Too many failed attempts. Try again later.") {
		t.Errorf("per-IP: %d %s", r.Code, r.Body)
	}
	// Another peer is blocked by the global limiter alone (all 5 slots used).
	r = e.ba("POST", "192.0.2.11:1", nil, bad)
	if r.Code != 429 {
		t.Errorf("global: %d", r.Code)
	}
	if _, ok := e.srv.backupIPLimiter.fails["192.0.2.11"]; ok {
		t.Error("per-IP attempt not released when global limit hit")
	}
}

func TestBackupAllGlobalCountedSeparately(t *testing.T) {
	e, _ := backupEnv(t)
	bad := map[string]string{"masterKey": "wrong-master-key-0000", "backupPassword": backupPW}
	for i, p := range []string{"192.0.2.1:1", "192.0.2.1:1", "192.0.2.2:1", "192.0.2.2:1", "192.0.2.3:1"} {
		if r := e.ba("POST", p, nil, bad); r.Code != 401 {
			t.Fatalf("attempt %d: %d", i, r.Code)
		}
	}
	if r := e.ba("POST", "192.0.2.4:1", nil, bad); r.Code != 429 {
		t.Errorf("global limit: %d", r.Code)
	}
}

func TestBackupAllBusy(t *testing.T) {
	e, mk := backupEnv(t)
	e.srv.backupAll.tryAcquire()
	r := e.ba("POST", okPeer, nil, good(mk))
	if r.Code != 429 || !strings.Contains(r.Body.String(), "Busy, try again.") {
		t.Errorf("busy: %d %s", r.Code, r.Body)
	}
	e.srv.backupAll.release()
	if r := e.ba("POST", okPeer, nil, good(mk)); r.Code != 200 {
		t.Errorf("after release: %d", r.Code)
	}
}

func readZipEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestBackupAllSuccess(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	e, mk := backupEnv(t)
	kim := e.register("kim", "password1")
	e.register("Zed", "password1") // no certs
	spec := map[string]any{"name": "web", "hosts": []string{"a.test"}, "bits": 2048, "days": 30}
	first := kim.expect(201, "POST", "/api/certs", spec)["id"].(string)
	kim.expect(201, "POST", "/api/certs/"+first+"/renew", nil)
	kim.expect(201, "POST", "/api/certs", map[string]any{"name": "db", "hosts": []string{"b.test"}, "bits": 2048, "days": 30})

	r := e.ba("POST", okPeer, nil, good(mk))
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/zip" ||
		!regexp.MustCompile(`^attachment; filename="certifire-backup-all_\d{4}-\d{2}-\d{2}\.zip"$`).MatchString(r.Header().Get("Content-Disposition")) {
		t.Fatalf("response %d %v", r.Code, r.Header())
	}
	outer := r.Body.Bytes()
	zr, err := yzip.NewReader(bytes.NewReader(outer), int64(len(outer)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "certifire-all.zip" || !zr.File[0].IsEncrypted() {
		t.Fatalf("outer: %v %v", err, zr)
	}
	open := func(pw string) ([]byte, error) {
		zr.File[0].SetPassword(pw)
		rc, err := zr.File[0].Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(rc)
	}
	if b, err := open("wrong password 0000"); err == nil {
		if _, err = zip.NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Error("wrong password opened the outer zip")
		}
	}
	inner, err := open(backupPW)
	if err != nil {
		t.Fatal(err)
	}
	users := readZipEntries(t, inner)
	var names []string
	for n := range users {
		names = append(names, n)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"Zed.zip", "admin.zip", "kim.zip"}) {
		t.Fatalf("users %v", names)
	}
	if n := len(readZipEntries(t, users["Zed.zip"])) + len(readZipEntries(t, users["admin.zip"])); n != 0 {
		t.Errorf("certless users have %d files", n)
	}
	files := readZipEntries(t, users["kim.zip"])
	names = nil
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"db.cnf", "db.crt", "db.key", "web.cnf", "web.crt", "web.key", "web_" + first + ".cnf", "web_" + first + ".crt", "web_" + first + ".key"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("kim files %v, want %v", names, want)
	}
	for _, base := range []string{"db", "web", "web_" + first} {
		block, _ := pem.Decode(files[base+".crt"])
		crt, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		kb, _ := pem.Decode(files[base+".key"])
		if kb == nil {
			t.Fatalf("%s.key not PEM", base)
		}
		var key crypto.Signer
		if k, err := x509.ParsePKCS8PrivateKey(kb.Bytes); err == nil {
			key = k.(crypto.Signer)
		} else if k, err := x509.ParsePKCS1PrivateKey(kb.Bytes); err == nil {
			key = k
		} else {
			t.Fatalf("%s.key unparsable", base)
		}
		type eq interface{ Equal(crypto.PublicKey) bool }
		if !key.Public().(eq).Equal(crt.PublicKey) {
			t.Errorf("%s: key does not match certificate", base)
		}
		if !strings.Contains(string(files[base+".cnf"]), "alt_names") {
			t.Errorf("%s.cnf unexpected", base)
		}
	}
	// A wrong key afterwards must not leak into logs either.
	e.ba("POST", okPeer, nil, map[string]string{"masterKey": "secret-wrong-master-key", "backupPassword": "secret-backup-password"})
	out := logs.String()
	for _, s := range []string{mk, backupPW, "secret-wrong-master-key", "secret-backup-password", "PRIVATE KEY"} {
		if strings.Contains(out, s) {
			t.Errorf("logs contain %q", s)
		}
	}
	if !strings.Contains(out, "result=ok") || !strings.Contains(out, "users=3") || !strings.Contains(out, "certs=3") || !strings.Contains(out, "result=bad-key") {
		t.Errorf("logs: %s", out)
	}
	// Success releases the throttle slots.
	if len(e.srv.backupIPLimiter.fails) != 1 { // only the bad-key attempt remains
		t.Errorf("limiter state %v", e.srv.backupIPLimiter.fails)
	}
}
