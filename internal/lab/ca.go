package lab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// Authority is an ephemeral Lab certificate authority. One is minted per
// world plan; it never reads production Secrets.
type Authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

// NewAuthority mints a Lab CA that issues per-host certificates for HTTPS
// fakes. The CA lives only for this plan.
func NewAuthority() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate Lab CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("Lab CA serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Sentinel Lab CA", Organization: []string{"sentinel-lab"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create Lab CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pemEncodeKey(key),
	}, nil
}

// CertPEM is the Lab CA certificate, for trust injection into copies.
func (a *Authority) CertPEM() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.certPEM...)
}

// KeyPEM is the Lab CA private key. Stored only as a Lab-namespace Secret.
func (a *Authority) KeyPEM() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.keyPEM...)
}

// Issue returns a certificate and key PEM for host, signed by the Lab CA.
func (a *Authority) Issue(host string) (certPEM, keyPEM []byte, err error) {
	if a == nil {
		return nil, nil, fmt.Errorf("no Lab CA")
	}
	if host == "" {
		return nil, nil, fmt.Errorf("Issue needs a host")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key for %s: %w", host, err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("serial for %s: %w", host, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"sentinel-lab-fake"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
		tmpl.DNSNames = nil
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, nil, fmt.Errorf("sign certificate for %s: %w", host, err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pemEncodeKey(key), nil
}

func pemEncodeKey(key *ecdsa.PrivateKey) []byte {
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		// The key was just generated; a marshal failure is a programmer error.
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
}

// LabCAName is the Lab-namespace Secret that holds the CA certificate.
const LabCAName = "lab-ca"

// tlsSecretName is the Lab Secret that holds a fake's host certificate.
func tlsSecretName(fakeService string) string {
	return fakeService + "-tls"
}
