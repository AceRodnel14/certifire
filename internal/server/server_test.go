package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"certifire/internal/certgen"
	"certifire/internal/store"
	"certifire/internal/storetest"
)

func testConfig(t *testing.T, env map[string]string) Config {
	t.Helper()
	cfg, err := LoadConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLoadConfig(t *testing.T) {
	cfg := testConfig(t, nil)
	if cfg.Port != "8080" || cfg.BaseURL != "http://localhost:8080" || cfg.SecureCookie() {
		t.Errorf("defaults: %+v", cfg)
	}
	if !testConfig(t, map[string]string{"BASE_URL": "https://app.example.com"}).SecureCookie() {
		t.Error("https base URL must set Secure cookies")
	}
}

func TestWithoutDatabase(t *testing.T) {
	h := New(testConfig(t, nil), nil)
	for path, want := range map[string]int{"/healthz": 200, "/readyz": 503, "/api/info": 200, "/api/state": 503, "/api/nope": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/info", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"base_url":"http://localhost:8080"}` {
		t.Errorf("info body %q", rec.Body)
	}
}

func TestHeadersAndSPA(t *testing.T) {
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>app</h1>")}, "app.js": {Data: []byte("js")}}
	h := New(testConfig(t, nil), static)
	for path, want := range map[string]string{"/": "<h1>app</h1>", "/settings": "<h1>app</h1>", "/app.js": "js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
		hd := rec.Header()
		if hd.Get("Content-Security-Policy") != "default-src 'self'; img-src 'self' data:; style-src 'self'; frame-ancestors 'none'" ||
			hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" || hd.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s headers: %v", path, hd)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("unknown api: %d %s", rec.Code, rec.Body)
	}
}

// --- integration harness (needs TEST_DATABASE_URL) ---

type env struct {
	t    *testing.T
	srv  *Server
	st   *store.Store
	hs   *httptest.Server
	hit  chan string // path of requests reaching the webhook stub
	stub *httptest.Server
	// stub controls
	status int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := storetest.New(t)
	srv := New(testConfig(t, nil), nil)
	srv.SetStore(st)
	e := &env{t: t, srv: srv, st: st, hit: make(chan string, 100), status: 204}
	e.hs = httptest.NewServer(srv)
	t.Cleanup(e.hs.Close)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hit <- r.URL.Path
		w.WriteHeader(e.status)
	}))
	t.Cleanup(stub.Close)
	e.stub = stub
	su, _ := url.Parse(stub.URL)
	srv.sender.Client.Transport = rewriteTo{su}
	return e
}

type rewriteTo struct{ to *url.URL }

func (r rewriteTo) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

// client is a cookie-keeping API client.
type client struct {
	e      *env
	cookie *http.Cookie
}

func (e *env) client() *client { return &client{e: e} }

func (c *client) do(method, path string, body any) (int, []byte, http.Header) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.hs.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			if ck.MaxAge < 0 {
				c.cookie = nil
			} else {
				c.cookie = ck
			}
		}
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func (c *client) expect(want int, method, path string, body any) map[string]any {
	c.e.t.Helper()
	code, b, _ := c.do(method, path, body)
	if code != want {
		c.e.t.Fatalf("%s %s: %d (want %d): %s", method, path, code, want, b)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (c *client) list(path string) []map[string]any {
	c.e.t.Helper()
	code, b, _ := c.do("GET", path, nil)
	if code != 200 {
		c.e.t.Fatalf("GET %s: %d %s", path, code, b)
	}
	var out []map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// setup runs first-run setup and returns the master key.
func (e *env) setup(adminPass string) string {
	e.t.Helper()
	m := e.client().expect(201, "POST", "/api/setup", map[string]string{"password": adminPass, "confirm": adminPass})
	return m["masterKey"].(string)
}

func (e *env) register(name, pass string) *client {
	e.t.Helper()
	c := e.client()
	c.expect(201, "POST", "/api/register", map[string]string{"username": name, "password": pass, "confirm": pass})
	return c
}

func (e *env) login(name, pass string) *client {
	e.t.Helper()
	c := e.client()
	c.expect(200, "POST", "/api/login", map[string]string{"username": name, "password": pass})
	return c
}

func TestSetupRegisterLogin(t *testing.T) {
	e := newEnv(t)
	anon := e.client()
	if m := anon.expect(200, "GET", "/api/state", nil); m["setupDone"] != false || m["user"] != nil {
		t.Errorf("fresh state %v", m)
	}
	anon.expect(409, "POST", "/api/register", map[string]string{"username": "alice", "password": "password1", "confirm": "password1"})
	anon.expect(400, "POST", "/api/setup", map[string]string{"password": "short", "confirm": "short"})
	anon.expect(400, "POST", "/api/setup", map[string]string{"password": "password1", "confirm": "password2"})

	mk := e.setup("adminpass1")
	if len(mk) != 20 {
		t.Errorf("master key %q", mk)
	}
	anon.expect(409, "POST", "/api/setup", map[string]string{"password": "adminpass1", "confirm": "adminpass1"})
	if m := anon.expect(200, "GET", "/api/state", nil); m["setupDone"] != true || m["user"] != nil {
		t.Errorf("state after setup (no session expected) %v", m)
	}

	alice := e.register("Alice", "password1")
	m := alice.expect(200, "GET", "/api/state", nil)
	u := m["user"].(map[string]any)
	if u["username"] != "Alice" || u["isAdmin"] != false || u["theme"].(map[string]any)["mode"] != "auto" {
		t.Errorf("state user %v", u)
	}
	for _, bad := range []string{"ab", "has space", "a/b", strings.Repeat("x", 33), "admin", "ALICE", "alice"} {
		code, _, _ := e.client().do("POST", "/api/register", map[string]string{"username": bad, "password": "password1", "confirm": "password1"})
		if code != 400 && code != 409 {
			t.Errorf("username %q: %d", bad, code)
		}
	}
	e.client().expect(401, "POST", "/api/login", map[string]string{"username": "alice", "password": "nope-nope"})
	e.client().expect(401, "POST", "/api/login", map[string]string{"username": "ghost", "password": "nope-nope"})
	a2 := e.login("ALICE", "password1")
	a2.expect(204, "POST", "/api/logout", nil)
	a2.expect(401, "GET", "/api/certs", nil)
	alice.expect(200, "GET", "/api/account/webhooks", nil) // other session unaffected
}

func TestCookieAttributes(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("bob", "password1")
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteLaxMode || c.cookie.Path != "/" || c.cookie.Secure {
		t.Errorf("cookie %+v", c.cookie)
	}
	if d := time.Until(c.cookie.Expires); d < 11*time.Hour || d > 12*time.Hour+time.Minute {
		t.Errorf("ttl %v", d)
	}
}

func TestLoginThrottle(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	e.register("carol", "password1")
	bad := map[string]string{"username": "carol", "password": "wrong-pass"}
	for i := range 5 {
		e.client().expect(401, "POST", "/api/login", bad)
		_ = i
	}
	e.client().expect(429, "POST", "/api/login", bad)
	// Even the right password is refused while locked.
	e.client().expect(429, "POST", "/api/login", map[string]string{"username": "carol", "password": "password1"})
	// Another user is unaffected; successful logins do not count.
	e.register("dave", "password1")
	for range 7 {
		e.login("dave", "password1")
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c1 := e.register("erin", "password1")
	c2 := e.login("erin", "password1")
	cert := c1.expect(201, "POST", "/api/certs", map[string]any{"name": "n", "hosts": []string{"a.test"}, "bits": 2048, "days": 10})
	id := cert["id"].(string)

	c1.expect(401, "POST", "/api/account/password", map[string]string{"currentPassword": "wrong-one", "password": "newpassword", "confirm": "newpassword"})
	c1.expect(400, "POST", "/api/account/password", map[string]string{"currentPassword": "password1", "password": "short", "confirm": "short"})
	c1.expect(204, "POST", "/api/account/password", map[string]string{"currentPassword": "password1", "password": "newpassword", "confirm": "newpassword"})

	c2.expect(401, "GET", "/api/certs", nil) // other sessions revoked
	c1.expect(200, "GET", "/api/certs", nil) // current kept
	e.client().expect(401, "POST", "/api/login", map[string]string{"username": "erin", "password": "password1"})
	c3 := e.login("erin", "newpassword")
	got := c3.expect(200, "GET", "/api/certs/"+id, nil) // key still decrypts under the same DEK
	if !strings.Contains(got["key"].(string), "PRIVATE KEY") {
		t.Error("key lost after password change")
	}
}

func TestChangePasswordThrottle(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("fay", "password1")
	body := map[string]string{"currentPassword": "wrong-one", "password": "newpassword", "confirm": "newpassword"}
	for range 5 {
		c.expect(401, "POST", "/api/account/password", body)
	}
	c.expect(429, "POST", "/api/account/password", body)
	body["currentPassword"] = "password1"
	c.expect(429, "POST", "/api/account/password", body)
}

func TestAdminResetAndUsers(t *testing.T) {
	e := newEnv(t)
	mk := e.setup("adminpass1")
	admin := e.login("admin", "adminpass1")
	gus := e.register("gus", "password1")
	id := gus.expect(201, "POST", "/api/certs", map[string]any{"name": "g", "hosts": []string{"g.test"}, "bits": 2048, "days": 5})["id"].(string)

	users := admin.list("/api/admin/users")
	if len(users) != 2 || users[0]["username"] != "admin" || users[1]["certCount"].(float64) != 1 {
		t.Errorf("users %v", users)
	}
	gus.expect(403, "GET", "/api/admin/users", nil)
	gus.expect(403, "POST", "/api/admin/users/1/password", map[string]string{})
	e.client().expect(401, "GET", "/api/admin/users", nil)

	gid := int(users[1]["id"].(float64))
	path := "/api/admin/users/" + itoa(gid) + "/password"
	good := map[string]string{"masterKey": mk, "password": "resetpass1", "confirm": "resetpass1"}
	admin.expect(404, "POST", "/api/admin/users/9999/password", good)
	admin.expect(400, "POST", path, map[string]string{"masterKey": mk, "password": "x", "confirm": "x"})
	admin.expect(403, "POST", path, map[string]string{"masterKey": "wrongwrongwrongwrong", "password": "resetpass1", "confirm": "resetpass1"})
	gus.expect(200, "GET", "/api/certs", nil) // still signed in
	admin.expect(204, "POST", path, good)
	gus.expect(401, "GET", "/api/certs", nil) // sessions revoked
	e.client().expect(401, "POST", "/api/login", map[string]string{"username": "gus", "password": "password1"})
	g2 := e.login("gus", "resetpass1")
	if k := g2.expect(200, "GET", "/api/certs/"+id, nil)["key"].(string); !strings.Contains(k, "PRIVATE KEY") {
		t.Error("key not recoverable after escrow reset")
	}
}

func TestAdminResetThrottle(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	admin := e.login("admin", "adminpass1")
	e.register("hal", "password1")
	path := "/api/admin/users/2/password"
	bad := map[string]string{"masterKey": "wrongwrongwrongwrong", "password": "resetpass1", "confirm": "resetpass1"}
	for range 5 {
		admin.expect(403, "POST", path, bad)
	}
	admin.expect(429, "POST", path, bad)
}

func TestCSRFAndBodyRules(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("ivy", "password1")
	post := func(hdr map[string]string, ct string, body string) int {
		req, _ := http.NewRequest("POST", e.hs.URL+"/api/account/password", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		req.AddCookie(c.cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(map[string]string{"Origin": "https://evil.test"}, "application/json", "{}"); code != 403 {
		t.Errorf("cross origin: %d", code)
	}
	if code := post(map[string]string{"Sec-Fetch-Site": "cross-site"}, "application/json", "{}"); code != 403 {
		t.Errorf("cross-site: %d", code)
	}
	if code := post(nil, "text/plain", "{}"); code != 415 {
		t.Errorf("text/plain: %d", code)
	}
	if code := post(nil, "application/json", strings.Repeat(" ", 70<<10)+"{}"); code != 400 {
		t.Errorf("oversize: %d", code)
	}
	if code := post(map[string]string{"Origin": e.hs.URL}, "application/json", "{bad"); code != 400 {
		t.Errorf("same origin bad json: %d", code)
	}
}

func TestTheme(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("joe", "password1")
	c.expect(204, "PUT", "/api/account/theme", map[string]any{"mode": "dark", "hue": 200})
	c.expect(400, "PUT", "/api/account/theme", map[string]any{"mode": "blue"})
	c.expect(400, "PUT", "/api/account/theme", map[string]any{"mode": "dark", "hue": 360})
	c.expect(400, "PUT", "/api/account/theme", map[string]any{"mode": "dark", "hue": -1})
	th := e.login("joe", "password1").expect(200, "GET", "/api/state", nil)["user"].(map[string]any)["theme"].(map[string]any)
	if th["mode"] != "dark" || th["hue"].(float64) != 200 {
		t.Errorf("theme %v", th)
	}
	c.expect(204, "PUT", "/api/account/theme", map[string]any{"mode": "auto", "hue": nil})
	if th := c.expect(200, "GET", "/api/state", nil)["user"].(map[string]any)["theme"].(map[string]any); th["hue"] != nil {
		t.Errorf("hue not cleared: %v", th)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// --- certificates ---

func TestCertLifecycle(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	a := e.register("kim", "password1")

	body := map[string]any{"name": "web", "hosts": []string{"App.Example.Test", "*.homelab", "192.0.2.5"}, "bits": 2048, "days": 30,
		"subject": map[string]string{"C": "de", "O": "Lab"}}
	cert := a.expect(201, "POST", "/api/certs", body)
	id := cert["id"].(string)
	if cert["name"] != "web" || cert["daysLeft"].(float64) < 29 || cert["renewedBy"] != nil || cert["subject"].(map[string]any)["C"] != "DE" {
		t.Errorf("created %v", cert)
	}
	if hosts := cert["hosts"].([]any); hosts[0] != "app.example.test" {
		t.Errorf("hosts %v", hosts)
	}

	// Stored key is ciphertext only.
	c, err := e.st.GetCert(context.Background(), 2, id)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(c.KeyEnc, []byte("PRIVATE KEY")) || len(c.KeyEnc) < 100 {
		t.Error("key not encrypted")
	}

	d := a.expect(200, "GET", "/api/certs/"+id, nil)
	for _, f := range []string{"crt", "key", "cnf", "subject", "notBefore", "notAfter", "daysLeft", "createdAt"} {
		if d[f] == nil {
			t.Errorf("detail lacks %s", f)
		}
	}
	if !strings.Contains(d["key"].(string), "PRIVATE KEY") || !strings.Contains(d["crt"].(string), "BEGIN CERTIFICATE") {
		t.Error("pem content")
	}
	_, _, hd := a.do("GET", "/api/certs/"+id, nil)
	if hd.Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control %q", hd.Get("Cache-Control"))
	}

	for ext, ct := range map[string]string{"crt": "application/x-pem-file", "key": "application/x-pem-file", "cnf": "text/plain"} {
		code, b, h := a.do("GET", "/api/certs/"+id+"/files/"+ext, nil)
		if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), ct) || h.Get("Content-Disposition") != `attachment; filename="web.`+ext+`"` || h.Get("Cache-Control") != "no-store" || len(b) == 0 {
			t.Errorf("file %s: %d %v", ext, code, h)
		}
	}
	a.expect(404, "GET", "/api/certs/"+id+"/files/pem", nil)

	// Isolation: another user always gets 404.
	b := e.register("lee", "password1")
	if len(b.list("/api/certs")) != 0 {
		t.Error("list leaks")
	}
	b.expect(404, "GET", "/api/certs/"+id, nil)
	b.expect(404, "GET", "/api/certs/"+id+"/files/key", nil)
	b.expect(404, "POST", "/api/certs/"+id+"/renew", nil)
	b.expect(404, "DELETE", "/api/certs/"+id, nil)
	b.expect(404, "POST", "/api/certs", map[string]any{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": 5, "renewOf": id})
	b.expect(404, "GET", "/api/certs/not-a-uuid", nil)

	// Renew.
	n := a.expect(201, "POST", "/api/certs/"+id+"/renew", nil)
	nid := n["id"].(string)
	if nid == id || n["name"] != "web" || n["bits"].(float64) != 2048 || n["days"].(float64) != 30 || n["subject"].(map[string]any)["O"] != "Lab" {
		t.Errorf("renewed %v", n)
	}
	if n["key"] == d["key"] {
		t.Error("renewal reused the key")
	}
	if a.expect(200, "GET", "/api/certs/"+id, nil)["renewedBy"] != nid {
		t.Error("renewedBy not set")
	}
	a.expect(409, "POST", "/api/certs/"+id+"/renew", nil)
	a.expect(409, "POST", "/api/certs", map[string]any{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": 5, "renewOf": id})

	// Edit & Renew from the new cert.
	er := a.expect(201, "POST", "/api/certs", map[string]any{"name": "web2", "hosts": []string{"z.test"}, "bits": 2048, "days": 7, "renewOf": nid})
	if er["name"] != "web2" || a.expect(200, "GET", "/api/certs/"+nid, nil)["renewedBy"] != er["id"] {
		t.Errorf("edit&renew %v", er)
	}
	if l := a.list("/api/certs"); len(l) != 3 || l[0]["id"] != er["id"] {
		t.Errorf("list order/len: %d", len(l))
	}

	// Deleting a renewal clears the old cert's renewedBy.
	a.expect(204, "DELETE", "/api/certs/"+er["id"].(string), nil)
	if a.expect(200, "GET", "/api/certs/"+nid, nil)["renewedBy"] != nil {
		t.Error("renewedBy not cleared on delete")
	}
	a.expect(404, "GET", "/api/certs/"+er["id"].(string), nil)
}

func TestCertDefaultsAndValidation(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("max", "password1")
	// Omitted bits/days use the defaults (generation at 4096 bits is slow, so only check it is accepted by validation path).
	spec := func(m map[string]any) int { code, _, _ := c.do("POST", "/api/certs", m); return code }
	bad := []map[string]any{
		{"name": "x", "hosts": []string{}, "bits": 2048, "days": 5},
		{"name": "x", "hosts": []string{"a.*.b"}, "bits": 2048, "days": 5},
		{"name": "x", "hosts": []string{"a.test"}, "bits": 1024, "days": 5},
		{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": 826},
		{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": -1},
		{"name": "a/b", "hosts": []string{"a.test"}, "bits": 2048, "days": 5},
		{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": 5, "subject": map[string]string{"O": "$HOME"}},
		{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": 5, "subject": map[string]string{"C": "DEU"}},
	}
	for i, m := range bad {
		if code := spec(m); code != 400 {
			t.Errorf("case %d: %d", i, code)
		}
	}
	if got := len(c.list("/api/certs")); got != 0 {
		t.Errorf("rejected requests stored %d certs", got)
	}
	// null bits/days and absent -> defaults 4096 / 825.
	r := c.expect(201, "POST", "/api/certs", map[string]any{"name": "dflt", "hosts": []string{"a.test"}, "bits": nil})
	if r["bits"].(float64) != 4096 || r["days"].(float64) != 825 {
		t.Errorf("defaults %v %v", r["bits"], r["days"])
	}
}

type busyGen struct{}

func (busyGen) Generate(context.Context, certgen.Spec) (*certgen.Result, error) {
	return nil, certgen.ErrBusy
}

func TestBusyReturns429(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("nat", "password1")
	e.srv.gen = busyGen{}
	c.expect(429, "POST", "/api/certs", map[string]any{"name": "x", "hosts": []string{"a.test"}, "bits": 2048, "days": 5})
	if len(c.list("/api/certs")) != 0 {
		t.Error("busy request stored a cert")
	}
}

func TestOriginBehindProxy(t *testing.T) {
	e := newEnv(t)
	e.srv.handler = securityHeaders(sameOrigin("https://app.example.com", limitBody(e.srv.routes(nil))))
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", e.hs.URL+"/api/logout", nil)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("https://app.example.com"); code != 204 {
		t.Errorf("BASE_URL origin: %d", code)
	}
	for _, o := range []string{"http://app.example.com", "https://evil.example.com", "https://app.example.com.evil.test"} {
		if code := post(o); code != 403 {
			t.Errorf("origin %s: %d", o, code)
		}
	}
}

func TestLimiterSweep(t *testing.T) {
	l := newLimiter(5, time.Minute)
	clock := time.Now()
	l.now = func() time.Time { return clock }
	l.take("a")
	l.take("b")
	clock = clock.Add(2 * time.Minute)
	l.take("c")
	l.sweep()
	if len(l.fails) != 1 {
		t.Errorf("keys after sweep: %d", len(l.fails))
	}
}

// --- webhooks ---

const (
	dTok = "aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-aB3_-"
	dURL = "https://discord.com/api/webhooks/123456789012345678/" + dTok
	tURL = "https://qa.environment.api.powerplatform.com/powerautomate/automations/direct/workflows/abc/triggers/manual/paths/invoke?api-version=1&sig=FAKESIG"
)

func TestWebhooks(t *testing.T) {
	e := newEnv(t)
	e.setup("adminpass1")
	c := e.register("ola", "password1")
	m := c.expect(200, "GET", "/api/account/webhooks", nil)
	if v, ok := m["discord"]; !ok || v != nil {
		t.Errorf("empty webhooks %v", m)
	}
	c.expect(404, "PUT", "/api/account/webhooks/slack", map[string]string{"url": dURL})

	w := c.expect(200, "PUT", "/api/account/webhooks/discord", map[string]string{"url": strings.Replace(dURL, "discord.com", "discordapp.com", 1)})
	if w["masked"] != "https://discord.com/…"+dURL[len(dURL)-4:] || w["broken"] != false {
		t.Errorf("view %v", w)
	}
	for _, u := range []string{"http://discord.com/api/webhooks/1/2", "https://discord.com.evil.test/x", "https://x@discord.com/api", "https://discord.com:8443/api/webhooks/123456789012345678/" + dTok, dURL + "?a=1", "https://192.0.2.1/x"} {
		c.expect(400, "PUT", "/api/account/webhooks/discord", map[string]string{"url": u})
	}
	c.expect(400, "PUT", "/api/account/webhooks/teams", map[string]string{"url": "https://x.logic.azure.com/workflows/a?sig=b"})
	c.expect(200, "PUT", "/api/account/webhooks/teams", map[string]string{"url": tURL})

	_, raw, _ := c.do("GET", "/api/account/webhooks", nil)
	if strings.Contains(string(raw), dTok) || strings.Contains(string(raw), "FAKESIG") || strings.Contains(string(raw), "webhooks/") {
		t.Errorf("full URL leaked: %s", raw)
	}

	// Test: delivered (discord 204), accepted (teams 202), rate limit 10 s.
	e.status = 204
	if r := c.expect(200, "POST", "/api/account/webhooks/discord/test", nil); r["outcome"] != "delivered" {
		t.Errorf("discord test %v", r)
	}
	if path := <-e.hit; !strings.HasSuffix(path, dTok) {
		t.Errorf("stub got %q", path)
	}
	c.expect(429, "POST", "/api/account/webhooks/discord/test", nil)
	e.srv.lastTest = map[int64]time.Time{}
	e.status = 202
	if r := c.expect(200, "POST", "/api/account/webhooks/teams/test", nil); r["outcome"] != "accepted" || !strings.Contains(r["message"].(string), "run history") {
		t.Errorf("teams test %v", r)
	}

	// Broken on 404, cleared by re-save.
	e.srv.lastTest = map[int64]time.Time{}
	e.status = 404
	r := c.expect(200, "POST", "/api/account/webhooks/discord/test", nil)
	if r["outcome"] != "broken" || !strings.Contains(r["message"].(string), "Discord 404") {
		t.Errorf("broken %v", r)
	}
	v := c.expect(200, "GET", "/api/account/webhooks", nil)["discord"].(map[string]any)
	if v["broken"] != true || v["brokenReason"] == nil || v["brokenAt"] == nil {
		t.Errorf("broken view %v", v)
	}
	if w := c.expect(200, "PUT", "/api/account/webhooks/discord", map[string]string{"url": dURL}); w["broken"] != false {
		t.Errorf("re-save did not clear broken: %v", w)
	}

	// Rate limited gives retryAfter seconds.
	e.srv.lastTest = map[int64]time.Time{}
	e.srv.sender.Client.Transport = roundFunc(func(*http.Request) (*http.Response, error) {
		h := http.Header{"Retry-After": {"3"}}
		return &http.Response{StatusCode: 429, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if r := c.expect(200, "POST", "/api/account/webhooks/discord/test", nil); r["outcome"] != "rate_limited" || r["retryAfter"].(float64) != 3 {
		t.Errorf("rate limited %v", r)
	}

	// A successful test clears the broken flag.
	e.srv.sender.Client.Transport = rewriteTo{mustURL(e.stubURL())}
	_ = e.st.MarkWebhookBroken(context.Background(), 2, "teams", "Teams 404: flow deleted or turned off")
	e.srv.lastTest = map[int64]time.Time{}
	e.status = 202
	c.expect(200, "POST", "/api/account/webhooks/teams/test", nil)
	if c.expect(200, "GET", "/api/account/webhooks", nil)["teams"].(map[string]any)["broken"] != false {
		t.Error("successful test did not clear broken")
	}

	c.expect(204, "DELETE", "/api/account/webhooks/discord", nil)
	c.expect(404, "POST", "/api/account/webhooks/discord/test", nil)
	if c.expect(200, "GET", "/api/account/webhooks", nil)["discord"] != nil {
		t.Error("not removed")
	}
	e.client().expect(401, "GET", "/api/account/webhooks", nil)
}

type roundFunc func(*http.Request) (*http.Response, error)

func (f roundFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func (e *env) stubURL() string { return e.stub.URL }
