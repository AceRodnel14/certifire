package certgen

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// ErrBusy means too many generations are running.
var ErrBusy = errors.New("certificate generation busy")

const (
	maxConcurrent = 2
	opensslLimit  = 60 * time.Second

	fileKey = "cert.key"
	fileCrt = "cert.crt"
	fileCnf = "cert.cnf"
)

// Result is a generated, verified certificate.
type Result struct {
	CertPEM, KeyPEM, CNF string
	NotBefore, NotAfter  time.Time
}

// Generator runs the openssl CLI.
type Generator struct {
	sem     chan struct{}
	timeout time.Duration
	openssl string
}

// NewGenerator returns a Generator that allows two concurrent runs.
func NewGenerator() *Generator {
	return &Generator{sem: make(chan struct{}, maxConcurrent), timeout: opensslLimit, openssl: "openssl"}
}

// Generate renders the .cnf, runs openssl in a private temp dir and verifies the
// result. Any mismatch is an error and nothing is returned.
func (g *Generator) Generate(ctx context.Context, spec Spec) (*Result, error) {
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	default:
		return nil, ErrBusy
	}

	dir, err := os.MkdirTemp("", "certifire-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	cnf := RenderCNF(spec)
	if err := os.WriteFile(filepath.Join(dir, fileCnf), []byte(cnf), 0o600); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.openssl, "req", "-x509", "-nodes",
		"-days", strconv.Itoa(spec.Days),
		"-newkey", "rsa:"+strconv.Itoa(spec.Bits),
		"-keyout", fileKey, "-out", fileCrt, "-config", fileCnf)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")} // no OPENSSL_CONF or other inherited env
	if out, err := cmd.CombinedOutput(); err != nil {
		// openssl output holds no secrets (the key goes to a file).
		slog.Error("openssl failed", "err", err, "output", string(out))
		return nil, fmt.Errorf("openssl: %w", err)
	}

	crt, err := os.ReadFile(filepath.Join(dir, fileCrt))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(dir, fileKey))
	if err != nil {
		return nil, err
	}
	cert, err := verify(spec, crt, key)
	if err != nil {
		return nil, err
	}
	return &Result{CertPEM: string(crt), KeyPEM: string(key), CNF: cnf, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}, nil
}

// verify checks the generated certificate against the validated input.
func verify(spec Spec, crtPEM, keyPEM []byte) (*x509.Certificate, error) {
	blk, _ := pem.Decode(crtPEM)
	if blk == nil {
		return nil, errors.New("no certificate PEM")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, err
	}
	var wantDNS, wantIP []string
	for _, h := range spec.Hosts {
		if h.IP {
			wantIP = append(wantIP, h.Value)
		} else {
			wantDNS = append(wantDNS, h.Value)
		}
	}
	var gotIP []string
	for _, ip := range cert.IPAddresses {
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			return nil, errors.New("bad IP in certificate")
		}
		gotIP = append(gotIP, a.Unmap().String())
	}
	if !slices.Equal(wantDNS, cert.DNSNames) || !slices.Equal(wantIP, gotIP) {
		return nil, errors.New("certificate SANs differ from the request")
	}
	if cert.Subject.CommonName != spec.Hosts[0].Value {
		return nil, errors.New("certificate CN differs from the request")
	}
	if !subjectMatches(spec.Subject, cert) {
		return nil, errors.New("certificate subject differs from the request")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errors.New("no key PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(*rsa.PrivateKey)
	if !ok || priv.N.BitLen() != spec.Bits || !priv.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("private key does not match the certificate")
	}
	return cert, nil
}

func subjectMatches(s Subject, c *x509.Certificate) bool {
	one := func(v []string) string {
		if len(v) == 1 {
			return v[0]
		}
		return ""
	}
	n := c.Subject
	return one(n.Country) == s.C && one(n.Province) == s.ST && one(n.Locality) == s.L &&
		one(n.Organization) == s.O && one(n.OrganizationalUnit) == s.OU &&
		len(n.Country) <= 1 && len(n.Province) <= 1 && len(n.Locality) <= 1 &&
		len(n.Organization) <= 1 && len(n.OrganizationalUnit) <= 1
}
