package capture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// Authority is a long-lived capture CA that issues per-host leaves for the
// production HTTPS MITM. Separate from the Lab CA: apps must trust this one
// explicitly when they opt into interception.
type Authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

// NewAuthority mints a capture CA.
func NewAuthority() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate capture CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Sentinel Capture CA", Organization: []string{"sentinel-capture"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{
		cert: cert, key: key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:  pemEncodeEC(key),
	}, nil
}

// AuthorityFromPEM loads a capture CA from PEM.
func AuthorityFromPEM(certPEM, keyPEM []byte) (*Authority, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("capture CA certificate PEM is missing")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("capture CA key PEM is missing")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &Authority{cert: cert, key: key, certPEM: append([]byte(nil), certPEM...), keyPEM: append([]byte(nil), keyPEM...)}, nil
}

// CertPEM is the CA certificate apps must trust.
func (a *Authority) CertPEM() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.certPEM...)
}

// KeyPEM is the CA private key.
func (a *Authority) KeyPEM() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.keyPEM...)
}

// TrustStorePKCS12 is a PKCS#12 truststore of the capture CA for JVM clients.
func (a *Authority) TrustStorePKCS12() ([]byte, error) {
	if a == nil || a.cert == nil {
		return nil, fmt.Errorf("no capture CA")
	}
	return pkcs12.Legacy.EncodeTrustStore([]*x509.Certificate{a.cert}, TrustStorePassword)
}

// Issue returns a leaf certificate for host.
func (a *Authority) Issue(host string) (certPEM, keyPEM []byte, err error) {
	if a == nil {
		return nil, nil, fmt.Errorf("no capture CA")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"sentinel-capture"}},
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
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pemEncodeEC(key), nil
}

func pemEncodeEC(key *ecdsa.PrivateKey) []byte {
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
}

type certCache struct {
	ca    *Authority
	mu    sync.Mutex
	certs map[string]*tls.Certificate
}

func (c *certCache) certificate(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.certs == nil {
		c.certs = map[string]*tls.Certificate{}
	}
	if cert, ok := c.certs[host]; ok {
		return cert, nil
	}
	certPEM, keyPEM, err := c.ca.Issue(host)
	if err != nil {
		return nil, err
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	c.certs[host] = &tlsCert
	return &tlsCert, nil
}
