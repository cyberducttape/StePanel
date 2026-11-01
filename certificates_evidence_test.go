package stepanel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestCertificateLineageEvidenceVerifiesHostAndExpiry(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "example.test"},
		DNSNames:  []string{"example.test", "www.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	lineage, err := certificateLineageEvidence("example.test", data)
	if err != nil || !lineage.Exists || !lineage.HostMatch || lineage.NotAfter.IsZero() || lineage.SHA256 == "" {
		t.Fatalf("valid lineage evidence = %+v, err=%v", lineage, err)
	}
	wrongHost, err := certificateLineageEvidence("other.test", data)
	if err != nil || wrongHost.HostMatch {
		t.Fatalf("hostname mismatch was accepted: %+v, err=%v", wrongHost, err)
	}
	if _, err := certificateLineageEvidence("example.test", []byte("not a certificate")); err == nil {
		t.Fatal("malformed lineage was accepted")
	}
}
