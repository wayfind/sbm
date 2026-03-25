package cmd

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
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

	// Migrate legacy node-level credentials to named user (entry only, idempotent)
	if usersDB != nil {
		if err := internal.EnsureDefaultUser(usersDB, meta); err != nil {
			log.Printf("[users] default user migration: %v", err)
		}
	}

	mux := http.NewServeMux()

	// Subscription endpoint (all users including hz-office go through per-user path)
	mux.HandleFunc("/sub/", makeUserSubHandler(usersDB))

	// Topology API endpoints
	mux.HandleFunc("/node-info", internal.BearerAuth(meta.APIToken, internal.HandleNodeInfo(meta)))
	mux.HandleFunc("/topology", internal.BearerAuth(meta.APIToken, internal.HandleTopology(meta, topoStore)))
	mux.HandleFunc("/report", internal.BearerAuth(meta.APIToken, internal.HandleReport(topoStore)))

	// Dashboard + API (entry only)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		mux.HandleFunc("/dashboard", internal.BearerAuth(meta.APIToken, handleDashboard))
		mux.HandleFunc("/api/users/", internal.BearerAuth(meta.APIToken, makeAPIUsersRouter(usersDB, meta)))
		mux.HandleFunc("/api/users", internal.BearerAuth(meta.APIToken, makeAPIUsersRouter(usersDB, meta)))
		mux.HandleFunc("/api/config/rules", internal.BearerAuth(meta.APIToken, handleConfigRules))
	}

	// Initialize CF topology client (if configured)
	var cfClient *internal.CFTopoClient
	if meta.CFWorkerURL != "" && meta.CFAPIKey != "" {
		cfClient = internal.NewCFTopoClient(meta.CFWorkerURL, meta.CFAPIKey)
		log.Printf("[cf] topology client initialized: %s", meta.CFWorkerURL)
	}

	// Start topology reporter (dual channel: CF + chain)
	internal.StartTopologyReporter(meta, topoStore, cfClient, 5*time.Minute)

	// Topology-driven auto-config (entry only — transit is pure relay, no reconcile needed)
	if meta.Role == internal.RoleEntry {
		internal.StartReconcileLoop(meta, topoStore, cfClient, usersDB, 1*time.Minute)
	}

	// Traffic collector + quota enforcement (entry only)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		internal.StartTrafficCollector(usersDB, 30*time.Second, func(disabled []string) {
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
		if username == "" {
			http.Error(w, "missing username in /sub/<username>", http.StatusBadRequest)
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

// makeAPIUsersRouter returns a handler that routes /api/users requests by method and subpath.
func makeAPIUsersRouter(db *sql.DB, meta *internal.Metadata) http.HandlerFunc {
	subPort := meta.SubPort
	if subPort == 0 {
		subPort = 20033
	}
	subBase := fmt.Sprintf("https://%s:%d/sub/", meta.Domain, subPort)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract username from path: /api/users/<name>[/action]
		subpath := strings.TrimPrefix(r.URL.Path, "/api/users")
		subpath = strings.TrimPrefix(subpath, "/")
		parts := strings.SplitN(subpath, "/", 2)
		name := parts[0]
		action := ""
		if len(parts) > 1 {
			action = parts[1]
		}

		switch {
		// GET /api/users — list all users
		case r.Method == "GET" && name == "":
			users, err := internal.ListUsers(db)
			if err != nil {
				jsonError(w, err.Error(), 500)
				return
			}
			// Inject sub_url for each user
			type userResponse struct {
				internal.UserWithTraffic
				SubURL string `json:"sub_url"`
			}
			resp := make([]userResponse, len(users))
			for i, u := range users {
				resp[i] = userResponse{u, subBase + u.Name}
			}
			internal.PrintJSONTo(w, resp)

		// POST /api/users — add user
		case r.Method == "POST" && name == "":
			var req struct {
				Name    string  `json:"name"`
				QuotaGB float64 `json:"quota_gb"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				jsonError(w, "invalid json", 400)
				return
			}
			if req.Name == "" {
				jsonError(w, "name required", 400)
				return
			}
			user, err := internal.AddUser(db, req.Name, req.QuotaGB)
			if err != nil {
				jsonError(w, err.Error(), 400)
				return
			}
			w.WriteHeader(201)
			internal.PrintJSONTo(w, user)

		// DELETE /api/users/<name> — delete user
		case r.Method == "DELETE" && name != "":
			if err := internal.DelUser(db, name); err != nil {
				jsonError(w, err.Error(), 400)
				return
			}
			internal.PrintJSONTo(w, map[string]string{"status": "ok"})

		// PUT /api/users/<name> — update user (quota_gb, enabled)
		case r.Method == "PUT" && name != "" && action == "":
			var req struct {
				QuotaGB *float64 `json:"quota_gb,omitempty"`
				Enabled *bool    `json:"enabled,omitempty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				jsonError(w, "invalid json", 400)
				return
			}
			if req.QuotaGB != nil {
				if err := internal.SetUserQuota(db, name, *req.QuotaGB); err != nil {
					jsonError(w, err.Error(), 400)
					return
				}
			}
			if req.Enabled != nil {
				if err := internal.SetUserEnabled(db, name, *req.Enabled); err != nil {
					jsonError(w, err.Error(), 400)
					return
				}
			}
			user, err := internal.GetUser(db, name)
			if err != nil {
				jsonError(w, err.Error(), 404)
				return
			}
			internal.PrintJSONTo(w, user)

		// POST /api/users/<name>/reset — reset traffic
		case r.Method == "POST" && name != "" && action == "reset":
			if err := internal.ResetUserTraffic(db, name); err != nil {
				jsonError(w, err.Error(), 400)
				return
			}
			// Also re-enable user after reset
			internal.SetUserEnabled(db, name, true)
			user, err := internal.GetUser(db, name)
			if err != nil {
				jsonError(w, err.Error(), 404)
				return
			}
			internal.PrintJSONTo(w, user)

		default:
			jsonError(w, "not found", 404)
		}
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleConfigRules returns current routing rules (read-only).
func handleConfigRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	internal.PrintJSONTo(w, map[string]interface{}{
		"ai_domains": internal.AIResidentialDomains,
		"rule_sets":  internal.BuildRuleSetDefs(),
	})
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
