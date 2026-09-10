package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8443", "address to serve on")
	certFile := flag.String("cert", "certs/tls.crt", "path to TLS certificate")
	keyFile := flag.String("key", "certs/tls.key", "path to TLS private key")
	flag.Parse()

	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("failed to load TLS cert/key (run cmd/gencert first?): %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/validate", handleValidate)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:      *addr,
		Handler:   mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
	}

	log.Printf("guardrail admission webhook listening on %s", *addr)
	// ListenAndServeTLS with empty cert/key paths here uses the
	// tls.Certificate already loaded into TLSConfig above, rather than
	// reading the files a second time.
	log.Fatal(server.ListenAndServeTLS("", ""))
}
