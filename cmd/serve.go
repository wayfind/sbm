package cmd

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sing-box-manager/internal"
	"strings"
	"syscall"
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

	// Initialize CF topology client (if configured)
	var cfClient *internal.CFTopoClient
	if meta.CFWorkerURL != "" && meta.CFAPIKey != "" {
		cfClient = internal.NewCFTopoClient(meta.CFWorkerURL, meta.CFAPIKey)
		log.Printf("[cf] topology client initialized: %s", meta.CFWorkerURL)
	}

	// Sync users from CF on startup (entry only, blocking)
	if meta.Role == internal.RoleEntry && usersDB != nil && cfClient != nil {
		if err := internal.SyncUsersFromCF(usersDB, cfClient); err != nil {
			log.Printf("[user-sync] initial sync error (continuing with local data): %v", err)
		}
	}

	// Subscription endpoint (all users including hz-office go through per-user path)
	mux.HandleFunc("/sub/", makeUserSubHandler(usersDB))

	// Topology API endpoints
	mux.HandleFunc("/node-info", internal.BearerAuth(meta.APIToken, internal.HandleNodeInfo(meta)))
	mux.HandleFunc("/topology", internal.BearerAuth(meta.APIToken, internal.HandleTopology(meta, topoStore)))
	mux.HandleFunc("/report", internal.BearerAuth(meta.APIToken, internal.HandleReport(topoStore)))

	// Dashboard + API (entry only)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		mux.HandleFunc("/dashboard", internal.BearerAuth(meta.APIToken, handleDashboard))
		mux.HandleFunc("/api/users/", internal.BearerAuth(meta.APIToken, makeAPIUsersRouter(usersDB, meta, cfClient)))
		mux.HandleFunc("/api/users", internal.BearerAuth(meta.APIToken, makeAPIUsersRouter(usersDB, meta, cfClient)))
		mux.HandleFunc("/api/config/rules", internal.BearerAuth(meta.APIToken, handleConfigRules))
	}

	// Start topology reporter (dual channel: CF + chain)
	internal.StartTopologyReporter(meta, topoStore, cfClient, 5*time.Minute)

	// Topology-driven auto-config (all roles: upstream discovery; entry/transit: config reconcile)
	internal.StartReconcileLoop(meta, topoStore, cfClient, usersDB, 1*time.Minute)

	// Traffic collector + quota enforcement (entry only)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		internal.StartTrafficCollector(usersDB, cfClient, meta.NodeID, 30*time.Second, func(disabled []string) {
			log.Printf("[traffic] quota exceeded, disabling users %v → reconcile now", disabled)
			// Trigger immediate reconcile: rebuild config without disabled users + restart sing-box
			downstreams := topoStore.GetDownstreams()
			if err := internal.ReconcileEntryConfig(meta, downstreams, usersDB, true); err != nil {
				log.Printf("[traffic] reconcile after quota disable failed: %v", err)
			}
		})
	}

	// User sync from CF (entry only, periodic)
	if meta.Role == internal.RoleEntry && usersDB != nil {
		internal.StartUserSyncLoop(usersDB, cfClient, 5*time.Minute)
	}

	// BWG bandwidth balancing (entry only)
	// Primary: topology-driven (reads BWG status from CF, no failover.json needed)
	// Fallback: failover.json (manual BWG credentials, for nodes without CF)
	if meta.Role == internal.RoleEntry {
		if cfClient != nil {
			internal.StartTopoRebalanceLoop(cfClient, 3) // daily at 03:00
		} else if failoverCfg, err := loadFailoverConfig(); err == nil && len(failoverCfg.Transits) > 0 {
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

	// Optional plaintext HTTP listener for /sub/ only.
	// Browsers warn on IP-based HTTPS (cert host mismatch) — this gives clients
	// a no-warning HTTP fallback for subscription fetch. Only the subscription
	// path is exposed; dashboard / API / report stay HTTPS-only.
	var httpSrv *http.Server
	if meta.HTTPSubPort > 0 && usersDB != nil {
		httpMux := http.NewServeMux()
		httpMux.HandleFunc("/sub/", makeUserSubHandler(usersDB))
		// Also serve the user-management API over HTTP for entry nodes. NAT'd
		// nodes (kunshan) present a cert whose CN is a domain that resolves
		// elsewhere, so the CF Worker can't validate TLS when reaching them by
		// IP — its fan-out writes (enable/reset) fall back to this HTTP port.
		// Still BearerAuth-protected; the node sits behind NAT.
		if meta.Role == internal.RoleEntry {
			apiHandler := internal.BearerAuth(meta.APIToken, makeAPIUsersRouter(usersDB, meta, cfClient))
			httpMux.HandleFunc("/api/users/", apiHandler)
			httpMux.HandleFunc("/api/users", apiHandler)
		}
		httpSrv = &http.Server{
			Addr:    fmt.Sprintf(":%d", meta.HTTPSubPort),
			Handler: stripServerHeader(httpMux),
		}
		log.Printf("HTTP subscription listener on :%d (/sub/ + /api/users)", meta.HTTPSubPort)
	}

	// Graceful shutdown on SIGINT/SIGTERM
	done := make(chan error, 2)
	go func() {
		done <- srv.ListenAndServeTLS("", "")
	}()
	if httpSrv != nil {
		go func() {
			done <- httpSrv.ListenAndServe()
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-done:
		return err
	case sig := <-quit:
		log.Printf("received %s, shutting down...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if httpSrv != nil {
			_ = httpSrv.Shutdown(ctx)
		}
		return srv.Shutdown(ctx)
	}
}

// stripServerHeader wraps a handler to explicitly remove the Server header.
func stripServerHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Del("Server")
		next.ServeHTTP(w, r)
	})
}

// findCertFiles returns cert/key paths, checking multiple locations.
func findCertFiles(domain string) (certFile, keyFile string) {
	if domain != "" {
		candidates := []struct{ cert, key string }{
			// Unified path (highest priority, the cross-node standard). Fixed
			// filenames, no domain subdir. Absent → os.Stat fails → next candidate,
			// so nodes still on certbot/root-cert are unaffected.
			{internal.UnifiedCertPath, internal.UnifiedCertKeyPath},
			// Certbot
			{"/etc/letsencrypt/live/" + domain + "/fullchain.pem", "/etc/letsencrypt/live/" + domain + "/privkey.pem"},
			// Pre-installed (sbm install / manual)
			{"/root/cert/" + domain + "/fullchain.pem", "/root/cert/" + domain + "/privkey.pem"},
			// Sing-box ACME under /root (common on non-root or systemd users)
			{"/root/.local/share/certmagic/certificates/acme-v02.api.letsencrypt.org-directory/" + domain + "/" + domain + ".crt",
				"/root/.local/share/certmagic/certificates/acme-v02.api.letsencrypt.org-directory/" + domain + "/" + domain + ".key"},
		}
		for _, c := range candidates {
			if _, err := os.Stat(c.cert); err == nil {
				return c.cert, c.key
			}
		}
	}
	// Sing-box ACME path (certmagic) — root-level fallback
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
func makeAPIUsersRouter(db *sql.DB, meta *internal.Metadata, cfClient *internal.CFTopoClient) http.HandlerFunc {
	// Build subscription URL bases for all entry nodes (self + others from CF KV)
	buildSubURLs := func(username string) []string {
		selfPort := meta.SubPort
		if selfPort == 0 {
			selfPort = 20033
		}
		urls := []string{fmt.Sprintf("https://%s:%d/sub/%s", meta.EffectiveHost(), selfPort, username)}
		if meta.HTTPSubPort > 0 {
			urls = append(urls, fmt.Sprintf("http://%s:%d/sub/%s", meta.EffectiveHost(), meta.HTTPSubPort, username))
		}

		if cfClient != nil {
			nodes, err := cfClient.GetAllNodes()
			if err == nil {
				for _, n := range nodes {
					if n.Role != internal.RoleEntry || n.NodeID == meta.NodeID {
						continue
					}
					host := n.PublicHost
					if host == "" {
						host = n.Domain
					}
					if host == "" {
						continue
					}
					port := n.SubPort
					if port == 0 {
						port = 20033
					}
					urls = append(urls, fmt.Sprintf("https://%s:%d/sub/%s", host, port, username))
					if n.HTTPSubPort > 0 {
						urls = append(urls, fmt.Sprintf("http://%s:%d/sub/%s", host, n.HTTPSubPort, username))
					}
				}
			}
		}
		return urls
	}

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
			type userResponse struct {
				internal.UserWithTraffic
				SubURLs []string `json:"sub_urls"`
			}
			resp := make([]userResponse, len(users))
			for i, u := range users {
				resp[i] = userResponse{u, buildSubURLs(u.Name)}
			}
			internal.PrintJSONTo(w, resp)

		// POST /api/users — add user (push to CF first, then local)
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
			// Create user locally first (generates UUID/password)
			user, err := internal.AddUser(db, req.Name, req.QuotaGB)
			if err != nil {
				jsonError(w, err.Error(), 400)
				return
			}
			// Push to CF — if fails, rollback local
			if cfClient != nil {
				if err := internal.PushUserToCF(db, cfClient, req.Name); err != nil {
					internal.DelUser(db, req.Name)
					jsonError(w, "CF 不可用，无法添加用户: "+err.Error(), 503)
					return
				}
			}
			w.WriteHeader(201)
			internal.PrintJSONTo(w, user)

		// DELETE /api/users/<name> — only allowed via CF Dashboard
		case r.Method == "DELETE" && name != "":
			jsonError(w, "请从 CF Dashboard 删除用户 (https://tun.vspeak.top/dashboard)", 403)

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
				// Manual enable is an explicit admin override of an auto quota-disable.
				if *req.Enabled {
					internal.SetQuotaDisabled(db, name, false)
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
			// Drop in-memory baseline so the next poll restarts cleanly.
			internal.ResetTrackerUser(name)
			// Also re-enable user after reset (clear both manual and quota disable)
			internal.SetUserEnabled(db, name, true)
			internal.SetQuotaDisabled(db, name, false)
			// Immediately re-report this node's usage so the CF aggregate drops
			// this user's old bytes right away (don't wait for the next ~30s poll).
			// This writes the same usage/{node_id} key the traffic collector
			// writes; the two are last-writer-wins. That's safe here — both read
			// the same SQLite, so whichever lands last carries equivalent data;
			// the reset already cleared this user's row, so neither can resurrect
			// the old bytes. On error we log but still return success: the next
			// poll will re-report, only the immediacy is lost.
			if cfClient != nil {
				month := internal.CurrentMonth()
				if usage, uErr := internal.GetMonthUsage(db, month); uErr == nil {
					if rErr := cfClient.ReportUsage(meta.NodeID, month, usage); rErr != nil {
						log.Printf("[api] reset %s: re-report usage failed (next poll will fix): %v", name, rErr)
					}
				} else {
					log.Printf("[api] reset %s: read usage failed: %v", name, uErr)
				}
			}
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

// handleConfigRules returns current routing policy (read-only).
func handleConfigRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	policy := internal.LoadRoutingPolicy()
	internal.PrintJSONTo(w, policy)
}

// handleDashboard serves a simple HTML dashboard.
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

const dashboardHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>sbm Dashboard</title>
<style>
body{font-family:system-ui;max-width:900px;margin:40px auto;padding:0 20px;background:#1a1a2e;color:#eee}
h1{color:#e94560}
table{width:100%;border-collapse:collapse;margin-top:20px}
th,td{padding:8px 12px;text-align:left;border-bottom:1px solid #333}
th{color:#e94560}
.enabled{color:#0f3}
.disabled{color:#f33}
.bar{background:#333;border-radius:4px;height:20px;position:relative}
.bar-fill{background:#e94560;height:100%;border-radius:4px;transition:width 0.3s}
.bar-text{position:absolute;top:0;left:8px;line-height:20px;font-size:12px}
.expand{cursor:pointer;user-select:none}
.expand:hover{color:#e94560}
.sub-row td{padding:4px 12px 4px 32px;border-bottom:1px solid #222;font-size:13px}
.sub-url{color:#8be;word-break:break-all;cursor:pointer}
.sub-url:hover{text-decoration:underline}
.copied{color:#0f3;font-size:11px;margin-left:8px;opacity:0;transition:opacity 0.3s}
</style></head><body>
<h1>sbm Dashboard</h1>
<table id="users"><thead><tr><th></th><th>用户</th><th>状态</th><th>用量</th><th>限额</th><th>进度</th></tr></thead><tbody></tbody></table>
<script>
function formatBytes(b){if(b>=1e12)return(b/1e12).toFixed(1)+' TB';if(b>=1e9)return(b/1e9).toFixed(1)+' GB';if(b>=1e6)return(b/1e6).toFixed(1)+' MB';return(b/1e3).toFixed(0)+' KB'}
function copyText(el,text){navigator.clipboard.writeText(text);const s=el.nextElementSibling;if(s){s.style.opacity='1';setTimeout(()=>s.style.opacity='0',1500)}}
let expanded={};
async function load(){
  const token=new URLSearchParams(location.search).get('token')||'';
  const r=await fetch('/api/users',{headers:{'Authorization':'Bearer '+token}});
  const users=await r.json();
  const tbody=document.querySelector('#users tbody');
  tbody.innerHTML='';
  for(const u of users||[]){
    const pct=u.quota_gb>0?Math.min(100,u.bytes_total/(u.quota_gb*1e9)*100):0;
    const urls=u.sub_urls||[];
    const hasUrls=urls.length>0;
    const isOpen=expanded[u.name];
    const tr=document.createElement('tr');
    tr.innerHTML='<td class="expand"'+(hasUrls?' onclick="expanded[\''+u.name+'\']=!expanded[\''+u.name+'\'];load()"':'')+' title="点击展开订阅链接">'+(hasUrls?(isOpen?'▼':'▶'):'')+'</td>'
      +'<td>'+u.name+'</td>'
      +'<td class="'+(u.enabled?'enabled':'disabled')+'">'+(u.enabled?'启用':'禁用')+'</td>'
      +'<td title="↑ '+formatBytes(u.bytes_up)+' / ↓ '+formatBytes(u.bytes_down)+'">'+formatBytes(u.bytes_total)+'</td>'
      +'<td>'+(u.quota_gb>0?u.quota_gb+' GB':'无限')+'</td>'
      +'<td><div class="bar"><div class="bar-fill" style="width:'+pct+'%"></div><div class="bar-text">'+pct.toFixed(0)+'%</div></div></td>';
    tbody.appendChild(tr);
    if(isOpen&&hasUrls){
      for(const url of urls){
        const sr=document.createElement('tr');
        sr.className='sub-row';
        sr.innerHTML='<td></td><td colspan="5"><span class="sub-url" onclick="copyText(this,\''+url+'\')">'+url+'</span><span class="copied">已复制</span></td>';
        tbody.appendChild(sr);
      }
    }
  }
}
load();setInterval(load,10000);
</script></body></html>`
