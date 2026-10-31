package certgen

import (
	"fmt"
	"strings"
)

// RenderCNF renders the openssl config from a validated Spec only.
func RenderCNF(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, `[ req ]
default_bits       = %d
prompt             = no
default_md         = sha256
distinguished_name = dn
x509_extensions    = v3_req

[ dn ]
`, s.Bits)
	for _, f := range []struct{ k, v string }{{"C", s.Subject.C}, {"ST", s.Subject.ST}, {"L", s.Subject.L}, {"O", s.Subject.O}, {"OU", s.Subject.OU}} {
		if f.v != "" {
			fmt.Fprintf(&b, "%s = %s\n", f.k, confEscape(f.v))
		}
	}
	fmt.Fprintf(&b, "CN = %s\n", s.Hosts[0].Value)
	b.WriteString(`
[ v3_req ]
subjectAltName       = @alt_names
basicConstraints     = critical, CA:TRUE, pathlen:0
keyUsage             = critical, digitalSignature, keyEncipherment, keyCertSign
extendedKeyUsage     = serverAuth
subjectKeyIdentifier = hash

[ alt_names ]
`)
	dns, ip := 0, 0
	for _, h := range s.Hosts {
		if h.IP {
			ip++
			fmt.Fprintf(&b, "IP.%d = %s\n", ip, h.Value)
		} else {
			dns++
			fmt.Fprintf(&b, "DNS.%d = %s\n", dns, h.Value)
		}
	}
	return b.String()
}

// confEscape protects the one character in the allowed subject set that
// openssl's config parser treats as a quote (it would silently drop it).
func confEscape(v string) string { return strings.ReplaceAll(v, "'", `\'`) }
