package isolationproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"time"
)

// GenerateCA returns a private CA for one relay lease. Persist the private key
// only in controller-owned 0600 storage, never in the CLI runtime or its workspace.
func GenerateCA() (certificatePEM, privateKeyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "gopod group egress lease"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), nil
}

func leafCertificates(certPEM, keyPEM []byte, hosts map[string]bool) (map[string]tls.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("invalid relay CA")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || time.Now().Before(ca.NotBefore) || !time.Now().Before(ca.NotAfter) {
		return nil, errors.New("invalid relay CA certificate")
	}
	if ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("relay certificate cannot sign")
	}
	out := map[string]tls.Certificate{}
	for host := range hosts {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		template := &x509.Certificate{SerialNumber: serial, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Minute), NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, pair.PrivateKey)
		if err != nil {
			return nil, err
		}
		out[host] = tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key, Leaf: template}
	}
	return out, nil
}
