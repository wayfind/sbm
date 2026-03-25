const DASHBOARD_HTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>sbm</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#0d1117;color:#e6edf3;min-height:100vh}
a{color:#58a6ff;text-decoration:none}

/* Header */
.header{display:flex;align-items:center;justify-content:space-between;padding:16px 24px;border-bottom:1px solid #30363d}
.header-left{display:flex;align-items:center;gap:12px}
.header h1{font-size:20px;color:#58a6ff;font-weight:700}
.header-stats{display:flex;gap:16px;font-size:13px;color:#8b949e}
.header-stats span{display:flex;align-items:center;gap:4px}

/* Nav tabs */
.nav{display:flex;gap:0;border-bottom:1px solid #30363d;padding:0 24px;background:#161b22}
.nav-tab{padding:10px 20px;font-size:14px;color:#8b949e;cursor:pointer;border-bottom:2px solid transparent;transition:all .15s;user-select:none}
.nav-tab:hover{color:#e6edf3}
.nav-tab.active{color:#58a6ff;border-bottom-color:#58a6ff}

/* Content */
.content{padding:24px;max-width:1400px;margin:0 auto}
.tab-pane{display:none}
.tab-pane.active{display:block}

/* Cards */
.card{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:16px}

/* Buttons */
.btn{padding:6px 14px;border-radius:6px;border:1px solid #30363d;background:#21262d;color:#e6edf3;cursor:pointer;font-size:13px;transition:all .15s}
.btn:hover{background:#30363d;border-color:#8b949e}
.btn-primary{background:#238636;border-color:#238636;color:#fff}
.btn-primary:hover{background:#2ea043}
.btn-danger{background:#da3633;border-color:#da3633;color:#fff}
.btn-danger:hover{background:#f85149}
.btn-sm{padding:3px 10px;font-size:12px;border-radius:4px;border:1px solid #30363d;background:#21262d;color:#e6edf3;cursor:pointer}
.btn-sm:hover{background:#30363d}
.btn-sm.btn-danger{background:transparent;border-color:#f85149;color:#f85149}
.btn-sm.btn-danger:hover{background:#f8514922}
.btn-icon{background:none;border:none;color:#8b949e;cursor:pointer;font-size:12px;padding:2px 4px}
.btn-icon:hover{color:#e6edf3}

/* Badges */
.role-badge{font-size:10px;padding:2px 8px;border-radius:12px;font-weight:600;text-transform:uppercase;letter-spacing:.5px}
.role-entry{background:#1f6feb33;color:#58a6ff}
.role-transit{background:#f0883e33;color:#f0883e}
.role-exit{background:#3fb95033;color:#3fb950}

/* Dots */
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.dot-alive{background:#3fb950}
.dot-stale{background:#d29922}
.dot-dead{background:#f85149}
.status-dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.status-dot.enabled{background:#3fb950}
.status-dot.disabled{background:#f85149}

/* ===== Tab 1: Topology ===== */
.topo-grid{display:grid;grid-template-columns:1fr 1fr 1fr;gap:24px;align-items:start}
.topo-col{display:flex;flex-direction:column;gap:12px}
.topo-col-title{font-size:12px;color:#8b949e;text-transform:uppercase;letter-spacing:1px;padding-bottom:8px;border-bottom:1px solid #30363d;text-align:center;font-weight:600}
.node{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:14px;transition:border-color .15s}
.node:hover{border-color:#58a6ff}
.node-header{display:flex;align-items:center;gap:8px;margin-bottom:6px}
.node-name{font-weight:600;font-size:14px}
.node-domain{font-size:12px;color:#8b949e;font-family:monospace;margin-bottom:6px}
.node-ports{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:6px}
.port{font-size:11px;font-family:monospace;color:#7ee787;background:#7ee78715;padding:2px 6px;border-radius:4px}
.port-exit{color:#f0883e;background:#f0883e15}
.node-ts{font-size:11px;color:#484f58}
.bwg-bar{background:#21262d;border-radius:4px;height:16px;margin-top:8px;position:relative;overflow:hidden}
.bwg-fill{height:100%;border-radius:4px;transition:width .3s}
.bwg-fill.ok{background:#238636}
.bwg-fill.warn{background:#9e6a03}
.bwg-fill.danger{background:#da3633}
.bwg-text{position:absolute;right:6px;top:50%;transform:translateY(-50%);font-size:10px;color:#e6edf3}
.topo-arrow{text-align:center;color:#30363d;font-size:20px;padding:4px 0}

/* ===== Tab 2: Users ===== */
.users-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:16px}
.users-header h2{font-size:16px;font-weight:600}
.users-table{width:100%;border-collapse:collapse;font-size:13px}
.users-table th{text-align:left;padding:8px 12px;color:#8b949e;font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:.5px;border-bottom:1px solid #30363d}
.users-table td{padding:8px 12px;border-bottom:1px solid #21262d}
.users-table tr:hover td{background:#161b2280}
.disabled-row{opacity:.5}
.user-name{font-weight:600}
.progress-bar{width:100%;max-width:120px;height:6px;background:#21262d;border-radius:3px;overflow:hidden}
.progress-fill{height:100%;border-radius:3px;background:#238636;transition:width .3s}
.progress-fill.warn{background:#9e6a03}
.progress-fill.danger{background:#da3633}
.user-detail td{background:#0d1117 !important;padding:12px 24px !important}
.detail-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:8px;font-size:13px}
.detail-grid b{color:#8b949e;font-weight:500;margin-right:6px}
.detail-grid code{background:#21262d;padding:2px 6px;border-radius:4px;font-size:12px;color:#7ee787;word-break:break-all}
.sub-url{cursor:pointer}
.sub-url:hover{color:#58a6ff}
.copy-hint{font-size:11px;color:#484f58;margin-left:4px}
.actions-cell{display:flex;gap:4px;flex-wrap:wrap}

/* ===== Tab 3: Rules ===== */
.rules-section h3{font-size:15px;font-weight:600;margin-bottom:12px;color:#e6edf3}
.domain-list{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:24px}
.domain-tag{font-size:12px;font-family:monospace;background:#1f6feb22;color:#58a6ff;padding:4px 10px;border-radius:14px;border:1px solid #1f6feb44}
.ruleset-table{width:100%;border-collapse:collapse;font-size:13px}
.ruleset-table th{text-align:left;padding:6px 12px;color:#8b949e;font-size:12px;border-bottom:1px solid #30363d}
.ruleset-table td{padding:6px 12px;border-bottom:1px solid #21262d}
.ruleset-table td a{color:#58a6ff;word-break:break-all}

/* ===== Tab 4: Install ===== */
.install-section{max-width:700px}
.install-section p{color:#8b949e;margin:4px 0;font-size:13px;line-height:1.6}
.cmd{background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:10px 14px;font-family:monospace;font-size:13px;color:#7ee787;margin:8px 0;position:relative;cursor:pointer;display:flex;align-items:center;justify-content:space-between}
.cmd:hover{border-color:#58a6ff}
.cmd .copy-btn{color:#484f58;font-size:11px;white-space:nowrap;margin-left:12px}
.cmd:hover .copy-btn{color:#58a6ff}
.cmd-label{color:#8b949e;font-size:12px;margin-top:12px;margin-bottom:2px}
.install-tabs{display:flex;gap:0;margin-bottom:16px;border-bottom:1px solid #30363d}
.install-tab{padding:8px 16px;font-size:13px;color:#8b949e;cursor:pointer;border-bottom:2px solid transparent;transition:all .15s}
.install-tab:hover{color:#e6edf3}
.install-tab.active{color:#58a6ff;border-bottom-color:#58a6ff}
.install-pane{display:none}
.install-pane.active{display:block}

/* Modal */
.modal{position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,.6);display:flex;align-items:center;justify-content:center;z-index:100}
.modal-content{background:#161b22;border:1px solid #30363d;border-radius:12px;padding:24px;width:380px;max-width:90vw}
.modal-content h3{margin-bottom:16px;font-size:16px}
.modal-content label{display:block;font-size:13px;color:#8b949e;margin:12px 0 4px}
.modal-content input{width:100%;padding:8px 12px;border:1px solid #30363d;border-radius:6px;background:#0d1117;color:#e6edf3;font-size:14px}
.modal-content input:focus{outline:none;border-color:#58a6ff}
.modal-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:20px}

/* Responsive */
@media(max-width:900px){
  .topo-grid{grid-template-columns:1fr;gap:16px}
  .content{padding:16px}
  .header{padding:12px 16px;flex-direction:column;gap:8px}
  .nav{padding:0 12px;overflow-x:auto}
}
</style></head><body>

<div class="header">
  <div class="header-left">
    <h1>sbm</h1>
  </div>
  <div class="header-stats" id="header-stats"></div>
</div>

<div class="nav">
  <div class="nav-tab active" onclick="switchTab('topology',this)">拓扑</div>
  <div class="nav-tab" onclick="switchTab('users',this)">用户</div>
  <div class="nav-tab" onclick="switchTab('rules',this)">路由</div>
  <div class="nav-tab" onclick="switchTab('install',this)">安装</div>
</div>

<div class="content">
  <!-- Tab 1: Topology -->
  <div class="tab-pane active" id="pane-topology">
    <div class="topo-grid" id="topo-grid">
      <div class="topo-col"><div class="topo-col-title">Entry</div><div id="col-entry"></div></div>
      <div class="topo-col"><div class="topo-col-title">Transit</div><div id="col-transit"></div></div>
      <div class="topo-col"><div class="topo-col-title">Exit</div><div id="col-exit"></div></div>
    </div>
  </div>

  <!-- Tab 2: Users -->
  <div class="tab-pane" id="pane-users">
    <div class="users-header">
      <h2>用户管理</h2>
      <button class="btn btn-primary" onclick="showAddUserModal()">+ 添加用户</button>
    </div>
    <div class="card" style="padding:0;overflow-x:auto">
      <table class="users-table">
        <thead><tr>
          <th>用户</th><th>状态</th><th>用量</th><th>配额</th><th>进度</th><th>操作</th>
        </tr></thead>
        <tbody id="users-body"></tbody>
      </table>
    </div>
    <div id="users-empty" style="display:none;text-align:center;padding:40px;color:#8b949e">暂无用户数据</div>
  </div>

  <!-- Tab 3: Rules -->
  <div class="tab-pane" id="pane-rules">
    <div class="rules-section">
      <h3>AI 住宅域名</h3>
      <p style="color:#8b949e;font-size:13px;margin-bottom:12px">匹配这些域名的流量走 Exit 出口（家宽 IP）</p>
      <div class="domain-list" id="ai-domains"></div>
      <h3 style="margin-top:24px">Rule Sets</h3>
      <p style="color:#8b949e;font-size:13px;margin-bottom:12px">sing-box rule_set 远程规则集</p>
      <div class="card" style="padding:0;overflow-x:auto">
        <table class="ruleset-table">
          <thead><tr><th>标签</th><th>出站</th><th>URL</th></tr></thead>
          <tbody id="rule-sets"></tbody>
        </table>
      </div>
    </div>
    <div id="rules-empty" style="display:none;text-align:center;padding:40px;color:#8b949e">暂无规则数据</div>
  </div>

  <!-- Tab 4: Install -->
  <div class="tab-pane" id="pane-install">
    <div class="install-section">
      <h2 style="font-size:18px;color:#58a6ff;margin-bottom:8px">快速安装</h2>
      <p>一条命令部署节点 — 自动下载 sbm + sing-box，配置并启动服务</p>

      <div class="install-tabs" style="margin-top:16px">
        <div class="install-tab active" onclick="showInstallTab('transit',this)">传输节点</div>
        <div class="install-tab" onclick="showInstallTab('exit',this)">出口节点</div>
        <div class="install-tab" onclick="showInstallTab('entry',this)">接入节点</div>
        <div class="install-tab" onclick="showInstallTab('download',this)">仅下载</div>
      </div>

      <div id="itab-transit" class="install-pane active">
        <p>CN2 GIA 传输节点，需要域名已解析到本机</p>
        <div class="cmd-label">安装命令</div>
        <div class="cmd" onclick="copyCmd(this)">
          <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --role transit --domain &lt;域名&gt;</span>
          <span class="copy-btn">点击复制</span>
        </div>
      </div>
      <div id="itab-exit" class="install-pane">
        <p>家宽出口节点，无需域名</p>
        <div class="cmd-label">安装命令</div>
        <div class="cmd" onclick="copyCmd(this)">
          <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --role exit</span>
          <span class="copy-btn">点击复制</span>
        </div>
      </div>
      <div id="itab-entry" class="install-pane">
        <p>国内云接入节点，需要域名和上游配置文件</p>
        <div class="cmd-label">安装命令</div>
        <div class="cmd" onclick="copyCmd(this)">
          <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --role entry --domain &lt;域名&gt; --upstreams &lt;文件&gt;</span>
          <span class="copy-btn">点击复制</span>
        </div>
      </div>
      <div id="itab-download" class="install-pane">
        <p>仅下载 sbm + sing-box 到 /usr/local/bin/，不配置节点</p>
        <div class="cmd-label">下载命令</div>
        <div class="cmd" onclick="copyCmd(this)">
          <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --download-only</span>
          <span class="copy-btn">点击复制</span>
        </div>
      </div>
      <p style="margin-top:16px;color:#484f58;font-size:12px">安装后编辑 /etc/sing-box/metadata.json 添加 name 和 api_token，然后 sbm status 验证</p>
    </div>
  </div>
</div>

<!-- Add User Modal -->
<div id="add-user-modal" class="modal" style="display:none" onclick="if(event.target===this)closeModal()">
  <div class="modal-content">
    <h3>添加用户</h3>
    <label>用户名</label>
    <input id="new-user-name" type="text" placeholder="alice">
    <label>月配额 (GB，0 = 无限)</label>
    <input id="new-user-quota" type="number" value="100" min="0">
    <div class="modal-actions">
      <button class="btn" onclick="closeModal()">取消</button>
      <button class="btn btn-primary" onclick="submitAddUser()">添加</button>
    </div>
  </div>
</div>

<script>
var token = new URLSearchParams(location.search).get('token') || '';
var currentTab = 'topology';
var nodesData = [];
var usersData = [];

/* === Tab switching === */
function switchTab(name, el) {
  currentTab = name;
  document.querySelectorAll('.nav-tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('.tab-pane').forEach(function(t) { t.classList.remove('active'); });
  el.classList.add('active');
  document.getElementById('pane-' + name).classList.add('active');
  if (name === 'topology') renderTopology();
  if (name === 'users') renderUsers();
  if (name === 'rules') loadRules();
}

function showInstallTab(name, el) {
  document.querySelectorAll('.install-tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('.install-pane').forEach(function(t) { t.classList.remove('active'); });
  el.classList.add('active');
  document.getElementById('itab-' + name).classList.add('active');
}

/* === Helpers === */
function ago(ts) {
  var s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
  if (s < 0) return 'just now';
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

function formatBytes(b) {
  if (!b || b === 0) return '0 B';
  var units = ['B', 'KB', 'MB', 'GB', 'TB'];
  var i = Math.floor(Math.log(b) / Math.log(1024));
  if (i >= units.length) i = units.length - 1;
  return (b / Math.pow(1024, i)).toFixed(i > 1 ? 1 : 0) + ' ' + units[i];
}

function copyCmd(el) {
  var text = el.querySelector('span').textContent;
  navigator.clipboard.writeText(text).then(function() {
    var btn = el.querySelector('.copy-btn');
    btn.textContent = '已复制!';
    setTimeout(function() { btn.textContent = '点击复制'; }, 1500);
  });
}

function copyText(el) {
  navigator.clipboard.writeText(el.textContent).then(function() {
    var hint = el.nextElementSibling;
    if (hint) { hint.textContent = '已复制!'; setTimeout(function() { hint.textContent = '点击复制'; }, 1500); }
  });
}

/* === API helper === */
function apiCall(method, path, body) {
  var opts = { method: method, headers: { 'X-API-Key': token } };
  if (body) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  return fetch(path, opts).then(function(r) { return r.json(); });
}

/* === Header stats === */
function updateStatusBar() {
  var alive = 0;
  var now = Date.now();
  for (var i = 0; i < nodesData.length; i++) {
    var ageMs = now - new Date(nodesData[i].reported_at).getTime();
    if (ageMs <= 20 * 60 * 1000) alive++;
  }
  var userCount = usersData.length;
  var html = '<span><span class="dot ' + (alive > 0 ? 'dot-alive' : 'dot-dead') + '"></span> ' + alive + ' 节点在线</span>';
  if (userCount > 0) html += '<span>' + userCount + ' 用户</span>';
  document.getElementById('header-stats').innerHTML = html;
}

/* === Tab 1: Topology === */
function renderTopology() {
  var colEntry = document.getElementById('col-entry');
  var colTransit = document.getElementById('col-transit');
  var colExit = document.getElementById('col-exit');
  colEntry.innerHTML = '';
  colTransit.innerHTML = '';
  colExit.innerHTML = '';
  var now = Date.now();

  for (var i = 0; i < nodesData.length; i++) {
    var n = nodesData[i];
    var ageMs = now - new Date(n.reported_at).getTime();
    var isAlive = ageMs <= 20 * 60 * 1000;
    var dotClass = isAlive ? 'dot-alive' : (ageMs <= 60 * 60 * 1000 ? 'dot-stale' : 'dot-dead');

    var inbs = (n.inbounds || []).filter(function(ib) { return !ib.for_exit; });
    var perExit = (n.inbounds || []).filter(function(ib) { return ib.for_exit; });

    var html = '<div class="node">';
    html += '<div class="node-header">';
    html += '<span class="dot ' + dotClass + '"></span>';
    html += '<span class="node-name">' + (n.name || n.node_id) + '</span>';
    html += '<span class="role-badge role-' + n.role + '">' + n.role + '</span>';
    html += '</div>';
    if (n.domain) html += '<div class="node-domain">' + n.domain + '</div>';
    if (inbs.length > 0) {
      html += '<div class="node-ports">';
      for (var j = 0; j < inbs.length; j++) {
        html += '<span class="port">' + inbs[j].protocol + ' :' + inbs[j].port + '</span>';
      }
      html += '</div>';
    }
    if (perExit.length > 0) {
      html += '<div class="node-ports">';
      for (var k = 0; k < perExit.length; k++) {
        html += '<span class="port port-exit">' + String.fromCharCode(8594) + ' ' + perExit[k].for_exit + ' :' + perExit[k].port + '</span>';
      }
      html += '</div>';
    }
    if (n.bwg_status) {
      var used = n.bwg_status.data_counter || 0;
      var plan = n.bwg_status.plan_monthly_data || 1;
      var pct = Math.min(100, Math.round(used / plan * 100));
      var remaining = 100 - pct;
      var barClass = remaining > 50 ? 'ok' : (remaining > 20 ? 'warn' : 'danger');
      html += '<div class="bwg-bar"><div class="bwg-fill ' + barClass + '" style="width:' + pct + '%"></div>';
      html += '<span class="bwg-text">' + remaining + '% 剩余</span></div>';
    }
    html += '<div class="node-ts">' + ago(n.reported_at) + '</div>';
    html += '</div>';

    var target = n.role === 'entry' ? colEntry : (n.role === 'transit' ? colTransit : colExit);
    target.innerHTML += html;
  }

  if (!colEntry.innerHTML) colEntry.innerHTML = '<div style="color:#484f58;text-align:center;padding:20px">无节点</div>';
  if (!colTransit.innerHTML) colTransit.innerHTML = '<div style="color:#484f58;text-align:center;padding:20px">无节点</div>';
  if (!colExit.innerHTML) colExit.innerHTML = '<div style="color:#484f58;text-align:center;padding:20px">无节点</div>';
}

/* === Tab 2: Users === */
function renderUsers() {
  var tbody = document.getElementById('users-body');
  var empty = document.getElementById('users-empty');
  if (!usersData || usersData.length === 0) {
    tbody.innerHTML = '';
    empty.style.display = 'block';
    return;
  }
  empty.style.display = 'none';
  var rows = '';
  for (var i = 0; i < usersData.length; i++) {
    var u = usersData[i];
    var pct = 0;
    if (u.quota_gb > 0) {
      pct = Math.min(100, Math.round((u.bytes_total || 0) / (u.quota_gb * 1073741824) * 100));
    }
    var pctClass = pct > 90 ? 'danger' : (pct > 70 ? 'warn' : '');
    rows += '<tr class="' + (u.enabled ? '' : 'disabled-row') + '">';
    rows += '<td><span class="user-name">' + u.name + '</span> ';
    rows += '<button class="btn-icon" onclick="toggleUserDetail(\\'' + u.name + '\\')">&#9662;</button></td>';
    rows += '<td><span class="status-dot ' + (u.enabled ? 'enabled' : 'disabled') + '"></span> ' + (u.enabled ? '启用' : '禁用') + '</td>';
    rows += '<td>' + formatBytes(u.bytes_total) + '</td>';
    rows += '<td>' + (u.quota_gb > 0 ? u.quota_gb + ' GB' : '无限') + '</td>';
    rows += '<td><div class="progress-bar"><div class="progress-fill ' + pctClass + '" style="width:' + pct + '%"></div></div></td>';
    rows += '<td><div class="actions-cell">';
    rows += '<button class="btn-sm" onclick="editQuota(\\'' + u.name + '\\',' + (u.quota_gb || 0) + ')">配额</button>';
    rows += '<button class="btn-sm" onclick="toggleUser(\\'' + u.name + '\\',' + u.enabled + ')">' + (u.enabled ? '禁用' : '启用') + '</button>';
    rows += '<button class="btn-sm btn-danger" onclick="deleteUser(\\'' + u.name + '\\')">删除</button>';
    rows += '<button class="btn-sm" onclick="resetTraffic(\\'' + u.name + '\\')">重置</button>';
    rows += '</div></td></tr>';

    // Detail row
    rows += '<tr class="user-detail" id="detail-' + u.name + '" style="display:none">';
    rows += '<td colspan="6"><div class="detail-grid">';
    if (u.uuid) rows += '<div><b>UUID</b> <code>' + u.uuid + '</code></div>';
    if (u.password) rows += '<div><b>密码</b> <code>' + u.password + '</code></div>';
    var subUrls = u.sub_urls || (u.sub_url ? [u.sub_url] : []);
    for (var si = 0; si < subUrls.length; si++) {
      rows += '<div><b>订阅' + (subUrls.length > 1 ? ' ' + (si + 1) : '') + '</b> <code class="sub-url" onclick="copyText(this)">' + subUrls[si] + '</code> <span class="copy-hint">点击复制</span></div>';
    }
    rows += '</div></td></tr>';
  }
  tbody.innerHTML = rows;
}

function toggleUserDetail(name) {
  var row = document.getElementById('detail-' + name);
  if (!row) return;
  row.style.display = row.style.display === 'none' ? '' : 'none';
}

function showAddUserModal() {
  document.getElementById('add-user-modal').style.display = 'flex';
  document.getElementById('new-user-name').value = '';
  document.getElementById('new-user-quota').value = '100';
  document.getElementById('new-user-name').focus();
}

function closeModal() {
  document.getElementById('add-user-modal').style.display = 'none';
}

function submitAddUser() {
  var name = document.getElementById('new-user-name').value.trim();
  if (!name) { alert('请输入用户名'); return; }
  var quota = parseFloat(document.getElementById('new-user-quota').value) || 0;
  apiCall('POST', '/api/users', { name: name, quota_gb: quota }).then(function(r) {
    if (r.error) { alert('错误: ' + r.error); return; }
    closeModal();
    loadUsers();
  });
}

function deleteUser(name) {
  if (!confirm('确认删除用户 ' + name + '？')) return;
  apiCall('DELETE', '/api/users/' + name).then(function() { loadUsers(); });
}

function editQuota(name, currentQuota) {
  var q = prompt('设置月配额 (GB，0=无限)', currentQuota);
  if (q === null) return;
  apiCall('PUT', '/api/users/' + name, { quota_gb: parseFloat(q) || 0 }).then(function() { loadUsers(); });
}

function toggleUser(name, currentEnabled) {
  apiCall('PUT', '/api/users/' + name, { enabled: !currentEnabled }).then(function() { loadUsers(); });
}

function resetTraffic(name) {
  if (!confirm('重置 ' + name + ' 的本月流量并启用？')) return;
  apiCall('POST', '/api/users/' + name + '/reset').then(function() { loadUsers(); });
}

/* === Tab 3: Rules === */
function loadRules() {
  fetch('/api/config/rules', { headers: { 'X-API-Key': token } }).then(function(r) {
    if (!r.ok) {
      document.getElementById('rules-empty').style.display = 'block';
      return;
    }
    return r.json();
  }).then(function(data) {
    if (!data) return;
    document.getElementById('rules-empty').style.display = 'none';
    renderRules(data);
  }).catch(function() {
    document.getElementById('rules-empty').style.display = 'block';
  });
}

function renderRules(data) {
  var domEl = document.getElementById('ai-domains');
  var rsEl = document.getElementById('rule-sets');
  var domains = data.ai_domains || data.domains || [];
  var ruleSets = data.rule_sets || [];

  var dhtml = '';
  for (var i = 0; i < domains.length; i++) {
    dhtml += '<span class="domain-tag">' + domains[i] + '</span>';
  }
  domEl.innerHTML = dhtml || '<span style="color:#484f58">无</span>';

  var rhtml = '';
  for (var j = 0; j < ruleSets.length; j++) {
    var rs = ruleSets[j];
    rhtml += '<tr><td>' + (rs.tag || '-') + '</td>';
    rhtml += '<td>' + (rs.outbound || '-') + '</td>';
    rhtml += '<td><a href="' + (rs.url || '#') + '" target="_blank">' + (rs.url || '-') + '</a></td></tr>';
  }
  rsEl.innerHTML = rhtml || '<tr><td colspan="3" style="color:#484f58;text-align:center;padding:16px">无</td></tr>';
}

/* === Data loading === */
function loadNodes() {
  fetch('/nodes', { headers: { 'X-API-Key': token } }).then(function(r) {
    if (!r.ok) return;
    return r.json();
  }).then(function(nodes) {
    if (!nodes) return;
    nodes.sort(function(a, b) {
      var o = { entry: 0, transit: 1, exit: 2 };
      return (o[a.role] !== undefined ? o[a.role] : 9) - (o[b.role] !== undefined ? o[b.role] : 9);
    });
    nodesData = nodes;
    if (currentTab === 'topology') renderTopology();
    updateStatusBar();
  }).catch(function() {});
}

function loadUsers() {
  fetch('/api/users', { headers: { 'X-API-Key': token } }).then(function(r) {
    if (!r.ok) return;
    return r.json();
  }).then(function(users) {
    if (!users) return;
    usersData = Array.isArray(users) ? users : (users.users || []);
    if (currentTab === 'users') renderUsers();
    updateStatusBar();
  }).catch(function() {});
}

loadNodes();
loadUsers();
setInterval(loadNodes, 10000);
setInterval(loadUsers, 30000);
</script></body></html>`;

// --- Helper: find entry node from TOPO_KV ---
async function findEntryNode(env) {
  const list = await env.TOPO_KV.list();
  for (const key of list.keys) {
    const val = await env.TOPO_KV.get(key.name);
    if (!val) continue;
    try {
      const node = JSON.parse(val);
      if (node.role === 'entry' && node.alive !== false) {
        const ageMs = Date.now() - new Date(node.reported_at).getTime();
        if (ageMs > 20 * 60 * 1000) continue; // skip stale
        const port = node.sub_port || 20033;
        return `https://${node.domain}:${port}`;
      }
    } catch {}
  }
  return null;
}

// --- Helper: proxy request to entry node ---
async function proxyToEntry(request, entryBase, path, apiKey) {
  const url = entryBase + path;
  const headers = new Headers();
  headers.set('Authorization', 'Bearer ' + apiKey);
  if (request.headers.get('Content-Type')) {
    headers.set('Content-Type', request.headers.get('Content-Type'));
  }
  // Do not forward Accept-Encoding — let CF Workers handle decompression
  const resp = await fetch(url, {
    method: request.method,
    headers,
    body: request.method !== 'GET' && request.method !== 'HEAD' ? request.body : undefined,
  });
  // Return with clean headers to avoid encoding issues
  const respHeaders = new Headers();
  respHeaders.set('Content-Type', resp.headers.get('Content-Type') || 'application/json');
  return new Response(resp.body, {
    status: resp.status,
    headers: respHeaders,
  });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const path = url.pathname;

    // --- Public routes (no auth) ---

    // Dashboard
    if (path === '/dashboard' || path === '/') {
      return new Response(DASHBOARD_HTML, {
        headers: { 'Content-Type': 'text/html; charset=utf-8' },
      });
    }

    // GET /install — serve install script from R2
    if (request.method === 'GET' && path === '/install') {
      const obj = await env.R2_BUCKET.get('releases/install.sh');
      if (!obj) return new Response('install.sh not found', { status: 404 });
      return new Response(obj.body, {
        headers: { 'Content-Type': 'text/plain; charset=utf-8' },
      });
    }

    // GET /releases/* — proxy R2 objects
    if (request.method === 'GET' && path.startsWith('/releases/')) {
      const key = path.slice(1); // "releases/..."
      const obj = await env.R2_BUCKET.get(key);
      if (!obj) return new Response('not found', { status: 404 });
      const contentType = key.endsWith('.json') ? 'application/json' : 'application/octet-stream';
      return new Response(obj.body, {
        headers: {
          'Content-Type': contentType,
          'Cache-Control': 'public, max-age=300',
        },
      });
    }

    // --- Authenticated routes ---

    const apiKey = env.API_KEY;
    const given = request.headers.get('X-API-Key') || url.searchParams.get('token') || '';
    if (!apiKey || given !== apiKey) {
      return Response.json({ error: 'unauthorized' }, { status: 401 });
    }

    // PUT /nodes/:id — upsert node info
    if (request.method === 'PUT' && path.startsWith('/nodes/')) {
      const id = path.slice(7);
      if (!id) return Response.json({ error: 'missing id' }, { status: 400 });
      const body = await request.text();
      try { JSON.parse(body); } catch {
        return Response.json({ error: 'invalid json' }, { status: 400 });
      }
      await env.TOPO_KV.put(id, body, { expirationTtl: 1800 }); // 30min TTL
      return Response.json({ status: 'ok' });
    }

    // GET /nodes — list all
    if (request.method === 'GET' && path === '/nodes') {
      const list = await env.TOPO_KV.list();
      const nodes = [];
      for (const key of list.keys) {
        const val = await env.TOPO_KV.get(key.name);
        if (val) {
          try { nodes.push(JSON.parse(val)); } catch {}
        }
      }
      return Response.json(nodes);
    }

    // DELETE /nodes/:id
    if (request.method === 'DELETE' && path.startsWith('/nodes/')) {
      const id = path.slice(7);
      await env.TOPO_KV.delete(id);
      return Response.json({ status: 'ok' });
    }

    // --- API proxy to entry node ---
    if (path.startsWith('/api/')) {
      const entryBase = await findEntryNode(env);
      if (!entryBase) {
        return Response.json({ error: 'no entry node available' }, { status: 502 });
      }
      return proxyToEntry(request, entryBase, path, given);
    }

    return Response.json({ error: 'not found' }, { status: 404 });
  },
};
