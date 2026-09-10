// gencert generates a self-signed TLS certificate for the webhook server.
// Run once, not on every server start -- the ValidatingWebhookConfiguration
// embeds this cert's raw bytes as its caBundle, so a fresh cert every
// restart would break trust between the API server and the webhook on the
// very next pod restart. A real production setup would use cert-manager for
// rotation; a decade-long validity window is a deliberate, stated shortcut
// for a local demo cluster, not an oversight.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	namespace   = "guardrail-system"
	serviceName = "guardrail-webhook"
)

func main() {
	outDir := "certs"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := run(outDir); err != nil {
		fmt.Fprintln(os.Stderr, "gencert:", err)
		os.Exit(1)
	}
}

func run(outDir string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generating serial: %w", err)
	}

	// Kubernetes services are reachable in-cluster under both the short
	// name and the fully-qualified one -- the API server calling this
	// webhook will use one or the other depending on how the
	// ValidatingWebhookConfiguration's clientConfig.service is set, so the
	// cert needs to answer to both.
	shortDNS := fmt.Sprintf("%s.%s.svc", serviceName, namespace)
	fqDNS := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace)

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fqDNS},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed: this cert is its own trust anchor
		DNSNames:              []string{shortDNS, fqDNS},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("creating certificate: %w", err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	certPath := filepath.Join(outDir, "tls.crt")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", certPath, err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshaling key: %w", err)
	}
	keyPath := filepath.Join(outDir, "tls.key")
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	// Private key never needs to be world-or-group readable, unlike the
	// certificate above which is public by design.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", keyPath, err)
	}

	fmt.Printf("wrote %s and %s, valid for %s and %s\n", certPath, keyPath, shortDNS, fqDNS)
	return nil
}
