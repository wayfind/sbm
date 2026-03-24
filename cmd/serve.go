package cmd

import (
	"crypto/tls"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sing-box-manager/internal"
	"strings"
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

	// Open users DB once (entry only, used by handlers and traffic collector)
	var usersDB *sql.DB
	if meta.Role == internal.RoleEntry {
		var dbErr error
		usersDB, dbErr = internal.OpenUsersDB()
		if dbErr != nil {
			log.Printf("[users] failed to open users DB: %v", dbErr)
		}
	}

	mux := http.NewServeMux()

	// Subscription endpoints
	mux.HandleFunc("/sub/hz-office", handleSub) // legacy
	mux.HandleFunc("/sub/", makeUserSubHandler(usersDB))

	// Topology API endpoints
	mux.HandleFunc("/node-info", internal.BearerAuth(meta.APIToken, internal.HandleNodeInfo(meta)))
	mux.HandleFunc("/topology", internal.BearerAuth(meta.APIToken, internal.HandleTopology(meta, topoStore)))
	mux.HandleFunc("/report", internal.BearerAuth(meta.APIToken, internal.HandleReport(topoStore)))

	// Dashboard (entry only)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		mux.HandleFunc("/dashboard", internal.BearerAuth(meta.APIToken, handleDashboard))
		mux.HandleFunc("/api/users", internal.BearerAuth(meta.APIToken, makeAPIUsersHandler(usersDB)))
	}

	// Initialize CF topology client (if configured)
	var cfClient *internal.CFTopoClient
	if meta.CFWorkerURL != "" && meta.CFAPIKey != "" {
		cfClient = internal.NewCFTopoClient(meta.CFWorkerURL, meta.CFAPIKey)
		log.Printf("[cf] topology client initialized: %s", meta.CFWorkerURL)
	}

	// Start topology reporter (dual channel: CF + chain)
	internal.StartTopologyReporter(meta, topoStore, cfClient, 30*time.Second)

	// Topology-driven auto-config (entry + transit)
	if meta.Role == internal.RoleEntry || meta.Role == internal.RoleTransit {
		internal.StartReconcileLoop(meta, topoStore, cfClient, 30*time.Second)
	}

	// Traffic collector + quota enforcement (entry only)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		internal.StartTrafficCollector(usersDB, "", 30*time.Second, func(disabled []string) {
			log.Printf("[traffic] quota exceeded, disabling users %v → restarting sing-box", disabled)
			// Restart sing-box so it drops connections from disabled users
			// (reconcile will regenerate config with only enabled users on next cycle)
			internal.ConfigMu.Lock()
			defer internal.ConfigMu.Unlock()
			internal.SystemctlOp("restart", "sing-box")
		})
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

// makeUserSubHandler returns a handler for per-user subscription: GET /sub/<username>
func makeUserSubHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/sub/")
		username := strings.TrimSuffix(path, "/")
		if username == "" || username == "hz-office" {
			handleSub(w, r)
			return
		}

		if db == nil {
			http.Error(w, "user management not available", http.StatusServiceUnavailable)
			return
		}

		sub, err := internal.GenerateSubscriptionForUser(db, username)
		if err != nil {
			log.Printf("generate subscription for %s: %v", username, err)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(err.Error()))
			return
		}

		data := []byte(sub)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		if r.Method == http.MethodGet {
			w.Write(data)
		}
	}
}

// makeAPIUsersHandler returns a handler for JSON user list.
func makeAPIUsersHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users, err := internal.ListUsers(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		internal.PrintJSONTo(w, users)
	}
}

// handleDashboard serves a simple HTML dashboard.
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>sbm Dashboard</title>
<style>
body{font-family:system-ui;max-width:800px;margin:40px auto;padding:0 20px;background:#1a1a2e;color:#eee}
h1{color:#e94560}
table{width:100%;border-collapse:collapse;margin-top:20px}
th,td{padding:8px 12px;text-align:left;border-bottom:1px solid #333}
th{color:#e94560}
.enabled{color:#0f3}
.disabled{color:#f33}
.bar{background:#333;border-radius:4px;height:20px;position:relative}
.bar-fill{background:#e94560;height:100%;border-radius:4px;transition:width 0.3s}
.bar-text{position:absolute;top:0;left:8px;line-height:20px;font-size:12px}
</style></head><body>
<h1>sbm Dashboard</h1>
<table id="users"><thead><tr><th>用户</th><th>状态</th><th>用量</th><th>限额</th><th>进度</th></tr></thead><tbody></tbody></table>
<script>
function formatBytes(b){if(b>=1e12)return(b/1e12).toFixed(1)+' TB';if(b>=1e9)return(b/1e9).toFixed(1)+' GB';if(b>=1e6)return(b/1e6).toFixed(1)+' MB';return(b/1e3).toFixed(0)+' KB'}
async function load(){
  const token=new URLSearchParams(location.search).get('token')||'';
  const r=await fetch('/api/users',{headers:{'Authorization':'Bearer '+token}});
  const users=await r.json();
  const tbody=document.querySelector('#users tbody');
  tbody.innerHTML='';
  for(const u of users||[]){
    const pct=u.quota_gb>0?Math.min(100,u.bytes_total/(u.quota_gb*1e9)*100):0;
    const tr=document.createElement('tr');
    tr.innerHTML='<td>'+u.name+'</td>'
      +'<td class="'+(u.enabled?'enabled':'disabled')+'">'+(u.enabled?'启用':'禁用')+'</td>'
      +'<td>'+formatBytes(u.bytes_total)+'</td>'
      +'<td>'+(u.quota_gb>0?u.quota_gb+' GB':'无限')+'</td>'
      +'<td><div class="bar"><div class="bar-fill" style="width:'+pct+'%"></div><div class="bar-text">'+pct.toFixed(0)+'%</div></div></td>';
    tbody.appendChild(tr);
  }
}
load();setInterval(load,10000);
</script></body></html>`
