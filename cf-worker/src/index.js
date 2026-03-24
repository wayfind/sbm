const DASHBOARD_HTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>sbm 全网拓扑</title>
<style>
*{box-sizing:border-box}
body{font-family:system-ui,-apple-system,sans-serif;margin:0;padding:20px;background:#0d1117;color:#e6edf3}
h1{color:#58a6ff;margin-bottom:4px}
.subtitle{color:#8b949e;margin-bottom:20px;font-size:14px}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:16px}
.card{background:#161b22;border:1px solid #30363d;border-radius:8px;padding:16px}
.card h3{margin:0 0 8px;display:flex;align-items:center;gap:8px}
.role{font-size:11px;padding:2px 8px;border-radius:12px;font-weight:600;text-transform:uppercase}
.role-entry{background:#1f6feb33;color:#58a6ff}
.role-transit{background:#f0883e33;color:#f0883e}
.role-exit{background:#3fb95033;color:#3fb950}
.dot{width:8px;height:8px;border-radius:50%;display:inline-block}
.dot-alive{background:#3fb950}
.dot-stale{background:#d29922}
.dot-dead{background:#f85149}
.info{font-size:13px;color:#8b949e;line-height:1.8}
.info b{color:#e6edf3}
.inbounds{margin-top:8px;font-size:12px;font-family:monospace;color:#7ee787}
.ts{font-size:11px;color:#484f58;margin-top:8px}
.status-bar{margin:16px 0;padding:12px;border-radius:8px;font-size:14px}
.status-ok{background:#238636;color:#fff}
.status-warn{background:#9e6a03;color:#fff}
.status-err{background:#da3633;color:#fff}
#updated{color:#484f58;font-size:12px;text-align:right}
.install-section{margin-top:24px;background:#161b22;border:1px solid #30363d;border-radius:8px;padding:20px}
.install-section h2{color:#58a6ff;margin:0 0 12px;font-size:18px}
.install-section p{color:#8b949e;margin:4px 0;font-size:13px}
.cmd{background:#0d1117;border:1px solid #30363d;border-radius:6px;padding:10px 14px;font-family:monospace;font-size:13px;color:#7ee787;margin:8px 0;position:relative;cursor:pointer;display:flex;align-items:center;justify-content:space-between}
.cmd:hover{border-color:#58a6ff}
.cmd .copy-btn{color:#484f58;font-size:11px;white-space:nowrap;margin-left:12px}
.cmd:hover .copy-btn{color:#58a6ff}
.cmd-label{color:#8b949e;font-size:12px;margin-top:12px;margin-bottom:2px}
.tab-bar{display:flex;gap:0;margin-bottom:16px;border-bottom:1px solid #30363d}
.tab{padding:8px 16px;font-size:13px;color:#8b949e;cursor:pointer;border-bottom:2px solid transparent;transition:all .15s}
.tab:hover{color:#e6edf3}
.tab.active{color:#58a6ff;border-bottom-color:#58a6ff}
.tab-content{display:none}
.tab-content.active{display:block}
</style></head><body>
<h1>sbm 全网拓扑</h1>
<div class="subtitle" id="updated"></div>
<div class="status-bar" id="status"></div>
<div class="grid" id="nodes"></div>

<div class="install-section">
<h2>快速安装</h2>
<p>一条命令部署节点 — 自动下载 sbm + sing-box，配置并启动服务</p>

<div class="tab-bar">
  <div class="tab active" onclick="showTab('transit',this)">传输节点</div>
  <div class="tab" onclick="showTab('exit',this)">出口节点</div>
  <div class="tab" onclick="showTab('entry',this)">接入节点</div>
  <div class="tab" onclick="showTab('download',this)">仅下载</div>
</div>

<div id="tab-transit" class="tab-content active">
  <p>CN2 GIA 传输节点，需要域名已解析到本机</p>
  <div class="cmd-label">安装命令</div>
  <div class="cmd" onclick="copyCmd(this)">
    <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --role transit --domain &lt;域名&gt;</span>
    <span class="copy-btn">点击复制</span>
  </div>
</div>

<div id="tab-exit" class="tab-content">
  <p>家宽出口节点，无需域名</p>
  <div class="cmd-label">安装命令</div>
  <div class="cmd" onclick="copyCmd(this)">
    <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --role exit</span>
    <span class="copy-btn">点击复制</span>
  </div>
</div>

<div id="tab-entry" class="tab-content">
  <p>国内云接入节点，需要域名和上游配置文件</p>
  <div class="cmd-label">安装命令</div>
  <div class="cmd" onclick="copyCmd(this)">
    <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --role entry --domain &lt;域名&gt; --upstreams &lt;文件&gt;</span>
    <span class="copy-btn">点击复制</span>
  </div>
</div>

<div id="tab-download" class="tab-content">
  <p>仅下载 sbm + sing-box 到 /usr/local/bin/，不配置节点</p>
  <div class="cmd-label">下载命令</div>
  <div class="cmd" onclick="copyCmd(this)">
    <span>curl -fsSL https://tun.vspeak.top/install | bash -s -- --download-only</span>
    <span class="copy-btn">点击复制</span>
  </div>
</div>

<p style="margin-top:16px;color:#484f58;font-size:12px">安装后编辑 /etc/sing-box/metadata.json 添加 name 和 api_token，然后 sbm status 验证</p>
</div>

<script>
function showTab(name,el){
  document.querySelectorAll('.tab').forEach(t=>t.classList.remove('active'));
  document.querySelectorAll('.tab-content').forEach(t=>t.classList.remove('active'));
  el.classList.add('active');
  document.getElementById('tab-'+name).classList.add('active');
}
function copyCmd(el){
  const text=el.querySelector('span').textContent;
  navigator.clipboard.writeText(text).then(()=>{
    const btn=el.querySelector('.copy-btn');
    btn.textContent='已复制!';
    setTimeout(()=>btn.textContent='点击复制',1500);
  });
}
const token=new URLSearchParams(location.search).get('token')||'';
function ago(ts){
  const s=Math.floor((Date.now()-new Date(ts))/1000);
  if(s<60)return s+'s ago';if(s<3600)return Math.floor(s/60)+'m ago';
  return Math.floor(s/3600)+'h ago';
}
async function load(){
  try{
    const r=await fetch('/nodes',{headers:{'X-API-Key':token}});
    if(!r.ok){document.getElementById('status').className='status-bar status-err';document.getElementById('status').textContent='认证失败';return}
    const nodes=await r.json();
    const grid=document.getElementById('nodes');
    grid.innerHTML='';
    const now=Date.now();
    let alive=0,stale=0;
    nodes.sort((a,b)=>{const o={entry:0,transit:1,exit:2};return(o[a.role]||9)-(o[b.role]||9)});
    for(const n of nodes){
      const ageMs=now-new Date(n.reported_at).getTime();
      const isStale=ageMs>20*60*1000;
      if(!isStale)alive++;else stale++;
      const card=document.createElement('div');card.className='card';
      const inbs=(n.inbounds||[]).filter(i=>!i.for_exit).map(i=>i.protocol+' :'+i.port).join(' · ');
      const perExit=(n.inbounds||[]).filter(i=>i.for_exit).map(i=>'→'+i.for_exit+' :'+i.port);
      card.innerHTML=
        '<h3><span class="dot '+(isStale?'dot-stale':'dot-alive')+'"></span>'
        +(n.name||n.node_id)+' <span class="role role-'+n.role+'">'+n.role+'</span></h3>'
        +'<div class="info">'
        +(n.domain?'<b>域名</b> '+n.domain+'<br>':'')
        +(n.address&&n.address!==n.domain?'<b>地址</b> '+n.address+'<br>':'')
        +'</div>'
        +(inbs?'<div class="inbounds">'+inbs+'</div>':'')
        +(perExit.length?'<div class="inbounds" style="color:#f0883e">'+perExit.join(' · ')+'</div>':'')
        +'<div class="ts">上报 '+ago(n.reported_at)+'</div>';
      grid.appendChild(card);
    }
    const sb=document.getElementById('status');
    if(stale===0&&alive>0){sb.className='status-bar status-ok';sb.textContent='✓ 全部在线 ('+alive+' 节点)';}
    else if(stale>0){sb.className='status-bar status-warn';sb.textContent='⚠ '+stale+' 节点失联, '+alive+' 在线';}
    else{sb.className='status-bar status-err';sb.textContent='✕ 无节点在线';}
    document.getElementById('updated').textContent='更新于 '+new Date().toLocaleTimeString();
  }catch(e){
    document.getElementById('status').className='status-bar status-err';
    document.getElementById('status').textContent='加载失败: '+e.message;
  }
}
load();setInterval(load,10000);
</script></body></html>`;

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const path = url.pathname;

    // Dashboard — auth via query token
    if (path === "/dashboard" || path === "/") {
      return new Response(DASHBOARD_HTML, {
        headers: { "Content-Type": "text/html; charset=utf-8" },
      });
    }

    // --- R2 release downloads (public, no auth) ---

    // GET /install — serve install script
    if (request.method === "GET" && path === "/install") {
      const obj = await env.R2_BUCKET.get("releases/install.sh");
      if (!obj) return new Response("install.sh not found", { status: 404 });
      return new Response(obj.body, {
        headers: { "Content-Type": "text/plain; charset=utf-8" },
      });
    }

    // GET /releases/* — proxy R2 objects
    if (request.method === "GET" && path.startsWith("/releases/")) {
      const key = path.slice(1); // "releases/..."
      const obj = await env.R2_BUCKET.get(key);
      if (!obj) return new Response("not found", { status: 404 });
      const contentType = key.endsWith(".json") ? "application/json" : "application/octet-stream";
      return new Response(obj.body, {
        headers: {
          "Content-Type": contentType,
          "Cache-Control": "public, max-age=300",
        },
      });
    }

    // API auth (everything below requires authentication)
    const apiKey = env.API_KEY;
    const given = request.headers.get("X-API-Key") || url.searchParams.get("token") || "";
    if (!apiKey || given !== apiKey) {
      return Response.json({ error: "unauthorized" }, { status: 401 });
    }

    // PUT /nodes/:id — upsert node info
    if (request.method === "PUT" && path.startsWith("/nodes/")) {
      const id = path.slice(7);
      if (!id) return Response.json({ error: "missing id" }, { status: 400 });
      const body = await request.text();
      // Validate JSON
      try { JSON.parse(body); } catch {
        return Response.json({ error: "invalid json" }, { status: 400 });
      }
      await env.TOPO_KV.put(id, body, { expirationTtl: 1800 }); // 30min TTL
      return Response.json({ status: "ok" });
    }

    // GET /nodes — list all
    if (request.method === "GET" && path === "/nodes") {
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
    if (request.method === "DELETE" && path.startsWith("/nodes/")) {
      const id = path.slice(7);
      await env.TOPO_KV.delete(id);
      return Response.json({ status: "ok" });
    }

    return Response.json({ error: "not found" }, { status: 404 });
  },
};
