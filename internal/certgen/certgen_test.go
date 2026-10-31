package certgen

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func req(hosts ...string) Request {
	return Request{Name: "web-1", Hosts: hosts, Bits: 2048, Days: 30}
}

func TestValidateHosts(t *testing.T) {
	long := strings.Repeat("a", 63)
	for name, tt := range map[string]struct {
		hosts   []string
		want    []string // normalised, when valid
		wantErr bool
	}{
		"dns":             {[]string{"App.Example.COM"}, []string{"app.example.com"}, false},
		"single label":    {[]string{"homelab"}, []string{"homelab"}, false},
		"trim and dedupe": {[]string{" a.test ", "A.TEST", "b.test"}, []string{"a.test", "b.test"}, false},
		"wildcard":        {[]string{"*.k8s.example.test"}, []string{"*.k8s.example.test"}, false},
		"wildcard single": {[]string{"*.homelab"}, []string{"*.homelab"}, false},
		"ipv4":            {[]string{"192.0.2.10"}, []string{"192.0.2.10"}, false},
		"ipv6 canonical":  {[]string{"2001:DB8:0:0::1"}, []string{"2001:db8::1"}, false},
		"ipv4 mapped":     {[]string{"::ffff:192.0.2.1"}, []string{"192.0.2.1"}, false},
		"label 63":        {[]string{"a.test", long + ".test"}, []string{"a.test", long + ".test"}, false},

		"none":           {nil, nil, true},
		"empty":          {[]string{"  "}, nil, true},
		"bare star":      {[]string{"*"}, nil, true},
		"star middle":    {[]string{"a.*.b"}, nil, true},
		"star prefix":    {[]string{"*foo.b"}, nil, true},
		"double star":    {[]string{"**.b"}, nil, true},
		"star twice":     {[]string{"*.*.b"}, nil, true},
		"star last":      {[]string{"a.*"}, nil, true},
		"underscore":     {[]string{"foo_bar.test"}, nil, true},
		"space":          {[]string{"foo bar.test"}, nil, true},
		"unicode":        {[]string{"bücher.test"}, nil, true},
		"label 64":       {[]string{long + "a.test"}, nil, true},
		"host 254":       {[]string{strings.Repeat("a.", 127) + "b"}, nil, true},
		"leading hyphen": {[]string{"-a.test"}, nil, true},
		"trailing dot":   {[]string{"a.test."}, nil, true},
		"empty label":    {[]string{"a..test"}, nil, true},
		"zone":           {[]string{"fe80::1%eth0"}, nil, true},
		"dollar":         {[]string{"$(id).test"}, nil, true},
		"expansion":      {[]string{"${ENV::X}.test"}, nil, true},
		"semicolon":      {[]string{"a;b.test"}, nil, true},
		"newline":        {[]string{"a.test\nbasicConstraints=CA:FALSE"}, nil, true},
		"path":           {[]string{"../etc"}, nil, true},
		"slash":          {[]string{"a/b"}, nil, true},
		"url":            {[]string{"https://a.test"}, nil, true},
		"port":           {[]string{"a.test:443"}, nil, true},
		"cn too long":    {[]string{strings.Repeat("a", 60) + ".example.test"}, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := req(tt.hosts...)
			spec, err := Validate(r)
			var ve *ValidationError
			if tt.wantErr {
				if !errors.As(err, &ve) {
					t.Fatalf("want ValidationError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := spec.HostValues(); !slices.Equal(got, tt.want) {
				t.Errorf("hosts %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHostLimitsAndFlags(t *testing.T) {
	var many []string
	for i := range 101 {
		many = append(many, "h"+strings.Repeat("a", i%5)+string(rune('a'+i%26))+".test")
	}
	// 101 distinct entries must be rejected before dedupe.
	if _, err := Validate(req(many...)); err == nil {
		t.Error("101 hosts accepted")
	}
	spec, err := Validate(req("*.homelab", "*.k8s.homelab", "a.test", "192.0.2.1"))
	if err != nil {
		t.Fatal(err)
	}
	var flags []bool
	for _, h := range spec.Hosts {
		flags = append(flags, h.SingleLabelWildcard)
	}
	if !slices.Equal(flags, []bool{true, false, false, false}) {
		t.Errorf("single-label flags %v", flags)
	}
}

func TestValidateScalars(t *testing.T) {
	base := func() Request { return req("a.test") }
	ok := func(mod func(*Request)) error { r := base(); mod(&r); _, err := Validate(r); return err }
	for name, tt := range map[string]struct {
		mod func(*Request)
		bad bool
	}{
		"ok":             {func(*Request) {}, false},
		"bits 3072":      {func(r *Request) { r.Bits = 3072 }, false},
		"bits 4096":      {func(r *Request) { r.Bits = 4096 }, false},
		"bits 1024":      {func(r *Request) { r.Bits = 1024 }, true},
		"bits 8192":      {func(r *Request) { r.Bits = 8192 }, true},
		"days 1":         {func(r *Request) { r.Days = 1 }, false},
		"days 825":       {func(r *Request) { r.Days = 825 }, false},
		"days 0":         {func(r *Request) { r.Days = 0 }, true},
		"days 826":       {func(r *Request) { r.Days = 826 }, true},
		"name empty":     {func(r *Request) { r.Name = "" }, true},
		"name slash":     {func(r *Request) { r.Name = "a/b" }, true},
		"name dotdot":    {func(r *Request) { r.Name = "../x" }, true},
		"name leading .": {func(r *Request) { r.Name = ".hidden" }, true},
		"name space":     {func(r *Request) { r.Name = "a b" }, true},
		"name newline":   {func(r *Request) { r.Name = "a\n" }, true},
		"name 64":        {func(r *Request) { r.Name = "a" + strings.Repeat("b", 63) }, false},
		"name 65":        {func(r *Request) { r.Name = "a" + strings.Repeat("b", 64) }, true},
		"C two":          {func(r *Request) { r.Subject.C = "de" }, false},
		"C one":          {func(r *Request) { r.Subject.C = "D" }, true},
		"C three":        {func(r *Request) { r.Subject.C = "DEU" }, true},
		"C digit":        {func(r *Request) { r.Subject.C = "D1" }, true},
		"O normal":       {func(r *Request) { r.Subject.O = "Bob's Lab (EU), Inc. & Co/+-" }, false},
		"O 65":           {func(r *Request) { r.Subject.O = strings.Repeat("a", 65) }, true},
		"O dollar":       {func(r *Request) { r.Subject.O = "$HOME" }, true},
		"O hash":         {func(r *Request) { r.Subject.O = "a#b" }, true},
		"O backslash":    {func(r *Request) { r.Subject.O = `a\b` }, true},
		"O quote":        {func(r *Request) { r.Subject.O = `a"b` }, true},
		"O semicolon":    {func(r *Request) { r.Subject.O = "a;b" }, true},
		"O equals":       {func(r *Request) { r.Subject.O = "a=b" }, true},
		"OU newline":     {func(r *Request) { r.Subject.OU = "a\n[x]" }, true},
		"L traversal ok": {func(r *Request) { r.Subject.L = "../" }, false}, // '/' and '.' are allowed characters; the value never reaches a path
		"ST unicode":     {func(r *Request) { r.Subject.ST = "Zürich" }, true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ok(tt.mod); (err != nil) != tt.bad {
				t.Errorf("err=%v, want bad=%v", err, tt.bad)
			}
		})
	}
	r := base()
	r.Subject = Subject{C: " de ", O: "  Lab "}
	spec, _ := Validate(r)
	if spec.Subject.C != "DE" || spec.Subject.O != "Lab" {
		t.Errorf("subject normalisation: %+v", spec.Subject)
	}
}

func TestDaysLeft(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for off, want := range map[time.Duration]int{
		7*24*time.Hour + time.Minute: 7,
		7 * 24 * time.Hour:           7,
		7*24*time.Hour - time.Second: 6,
		time.Hour:                    0,
		0:                            0,
		-time.Second:                 -1,
		-24 * time.Hour:              -1,
		-25 * time.Hour:              -2,
	} {
		if got := DaysLeft(now.Add(off), now); got != want {
			t.Errorf("offset %v: %d, want %d", off, got, want)
		}
	}
}

func TestRenderCNF(t *testing.T) {
	spec, err := Validate(Request{Name: "n", Bits: 3072, Days: 10,
		Hosts:   []string{"a.test", "*.k8s.test", "192.0.2.1", "2001:db8::1", "b.test"},
		Subject: Subject{C: "de", O: "Lab"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `[ req ]
default_bits       = 3072
prompt             = no
default_md         = sha256
distinguished_name = dn
x509_extensions    = v3_req

[ dn ]
C = DE
O = Lab
CN = a.test

[ v3_req ]
subjectAltName       = @alt_names
basicConstraints     = critical, CA:TRUE, pathlen:0
keyUsage             = critical, digitalSignature, keyEncipherment, keyCertSign
extendedKeyUsage     = serverAuth
subjectKeyIdentifier = hash

[ alt_names ]
DNS.1 = a.test
DNS.2 = *.k8s.test
IP.1 = 192.0.2.1
IP.2 = 2001:db8::1
DNS.3 = b.test
`
	if got := RenderCNF(spec); got != want {
		t.Errorf("cnf mismatch:\n%s", got)
	}
	empty, _ := Validate(req("a.test"))
	if strings.Contains(RenderCNF(empty), "C = ") || strings.Contains(RenderCNF(empty), "OU") {
		t.Error("empty subject fields must be omitted")
	}
}

func needOpenssl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
}

func TestGenerate(t *testing.T) {
	needOpenssl(t)
	spec, err := Validate(Request{Name: "n", Bits: 2048, Days: 30,
		Hosts:   []string{"app.example.test", "*.k8s.example.test", "*.homelab", "192.0.2.7", "2001:db8::7"},
		Subject: Subject{C: "DE", ST: "Bob's State", L: "A (B), C & D/E+F-G.", O: "Lab", OU: "Ops"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewGenerator().Generate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode([]byte(res.CertPEM))
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.IsCA || cert.MaxPathLen != 0 || !cert.MaxPathLenZero || !cert.BasicConstraintsValid {
		t.Errorf("basic constraints: %+v", cert)
	}
	if cert.KeyUsage != x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment|x509.KeyUsageCertSign {
		t.Errorf("key usage %v", cert.KeyUsage)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(cert.SubjectKeyId) == 0 {
		t.Error("eku/ski")
	}
	if d := cert.NotAfter.Sub(cert.NotBefore); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("validity %v", d)
	}
	if !res.NotAfter.Equal(cert.NotAfter) || !strings.Contains(res.KeyPEM, "PRIVATE KEY") || res.CNF != RenderCNF(spec) {
		t.Error("result fields")
	}
	if cert.Subject.Organization[0] != "Lab" || cert.Subject.Province[0] != "Bob's State" {
		t.Errorf("subject %v", cert.Subject)
	}
}

func TestGenerateInjectionStringsNeverReachOpenssl(t *testing.T) {
	needOpenssl(t)
	// Everything that could break out of the config must be rejected before generation.
	for _, h := range []string{"a.test\n[ v3_req ]", "$(touch x).test", "a.test;id", "${ENV::HOME}", "a.test #", "a\\b"} {
		if _, err := Validate(req(h)); err == nil {
			t.Errorf("%q accepted", h)
		}
	}
}

func TestGenerateBusyAndTimeout(t *testing.T) {
	g := NewGenerator()
	g.sem <- struct{}{}
	g.sem <- struct{}{}
	spec, _ := Validate(req("a.test"))
	if _, err := g.Generate(context.Background(), spec); !errors.Is(err, ErrBusy) {
		t.Errorf("want ErrBusy, got %v", err)
	}
	needOpenssl(t)
	g = NewGenerator()
	g.timeout = time.Millisecond
	spec.Bits = 4096
	if _, err := g.Generate(context.Background(), spec); err == nil {
		t.Error("timeout not enforced")
	}
}

func TestGenerateConcurrentLimit(t *testing.T) {
	needOpenssl(t)
	g := NewGenerator()
	spec, _ := Validate(req("a.test"))
	var wg sync.WaitGroup
	var mu sync.Mutex
	busy, okc := 0, 0
	for range 6 {
		wg.Go(func() {
			_, err := g.Generate(context.Background(), spec)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, ErrBusy) {
				busy++
			} else if err == nil {
				okc++
			}
		})
	}
	wg.Wait()
	if okc < 2 || okc+busy != 6 {
		t.Errorf("ok=%d busy=%d", okc, busy)
	}
}

func TestVerifyRejectsMismatch(t *testing.T) {
	needOpenssl(t)
	spec, _ := Validate(req("a.test"))
	res, err := NewGenerator().Generate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := Validate(req("b.test"))
	if _, err := verify(other, []byte(res.CertPEM), []byte(res.KeyPEM)); err == nil {
		t.Error("SAN mismatch accepted")
	}
	other2, _ := Validate(Request{Name: "n", Hosts: []string{"a.test"}, Bits: 2048, Days: 5, Subject: Subject{O: "X"}})
	if _, err := verify(other2, []byte(res.CertPEM), []byte(res.KeyPEM)); err == nil {
		t.Error("subject mismatch accepted")
	}
	other3, _ := Validate(Request{Name: "n", Hosts: []string{"a.test"}, Bits: 3072, Days: 5})
	if _, err := verify(other3, []byte(res.CertPEM), []byte(res.KeyPEM)); err == nil {
		t.Error("bits mismatch accepted")
	}
}

func TestConfEscape(t *testing.T) {
	if got := confEscape("Bob's"); got != `Bob\'s` {
		t.Errorf("got %q", got)
	}
}
