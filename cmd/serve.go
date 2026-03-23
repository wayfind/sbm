package cmd

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sing-box-manager/internal"
)

func RunServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 0, "监听端口 (默认从 metadata 读取)")
	fs.Parse(args)

	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}

	listenPort := meta.SubPort
	if listenPort == 0 {
		listenPort = 20032
	}
	if *port != 0 {
		listenPort = *port
	}

	certFile := internal.CertPath(meta.Domain)
	keyFile := internal.CertKeyPath(meta.Domain)

	mux := http.NewServeMux()
	mux.HandleFunc("/sub/hz-office", handleSub)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", listenPort),
		Handler: stripServerHeader(mux),
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					return nil, err
				}
				return &cert, nil
			},
		},
	}

	log.Printf("subscription server listening on :%d", listenPort)
	return srv.ListenAndServeTLS("", "")
}

func handleSub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sub, err := internal.GenerateSubscription()
	if err != nil {
		log.Printf("generate subscription error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	data := []byte(sub)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	if r.Method == http.MethodGet {
		w.Write(data)
	}
}

// stripServerHeader wraps a handler to explicitly remove the Server header.
func stripServerHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Del("Server")
		next.ServeHTTP(w, r)
	})
}
