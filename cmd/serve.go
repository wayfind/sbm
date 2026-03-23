package cmd

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sing-box-manager/internal"
	"time"
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
		listenPort = 20033
	}
	if *port != 0 {
		listenPort = *port
	}

	// Try certbot cert first, fallback to sing-box ACME cert
	certFile, keyFile := findCertFiles(meta.Domain)

	// Topology store for aggregating downstream reports
	topoStore := internal.NewTopoStore()

	mux := http.NewServeMux()

	// Subscription endpoint (existing)
	mux.HandleFunc("/sub/hz-office", handleSub)

	// Topology API endpoints
	mux.HandleFunc("/node-info", internal.BearerAuth(meta.APIToken, internal.HandleNodeInfo(meta)))
	mux.HandleFunc("/topology", internal.BearerAuth(meta.APIToken, internal.HandleTopology(meta, topoStore)))
	mux.HandleFunc("/report", internal.BearerAuth(meta.APIToken, internal.HandleReport(topoStore)))

	// Start topology reporter (reports to upstreams)
	internal.StartTopologyReporter(meta, topoStore, 30*time.Second)

	// Topology-driven auto-config (entry + transit)
	if meta.Role == internal.RoleEntry || meta.Role == internal.RoleTransit {
		internal.StartReconcileLoop(meta, topoStore, 30*time.Second)
	}

	// BWG bandwidth balancing (entry only)
	if meta.Role == internal.RoleEntry {
		if failoverCfg, err := loadFailoverConfig(); err == nil && len(failoverCfg.Transits) > 0 {
			internal.StartFailoverLoop(failoverCfg, 1*time.Hour)
		}
	}

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

	log.Printf("subscription server listening on :%d (role=%s)", listenPort, meta.Role)
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

// findCertFiles returns cert/key paths, preferring certbot over sing-box ACME.
func findCertFiles(domain string) (certFile, keyFile string) {
	if domain != "" {
		// Certbot path
		cb := "/etc/letsencrypt/live/" + domain
		if _, err := os.Stat(cb + "/fullchain.pem"); err == nil {
			return cb + "/fullchain.pem", cb + "/privkey.pem"
		}
	}
	// Sing-box ACME path
	return internal.CertPath(domain), internal.CertKeyPath(domain)
}
