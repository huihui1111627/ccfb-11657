package web

// indexHTML 是 BFD 仿真仪表盘单页。五个区块:会话列表、状态机、收发时间线、
// 检测时间统计、链路切换。原生 JS + 内嵌样式,无外部依赖。
const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>BFD 链路仿真 · 缆车控制网</title>
<style>
:root{--bg:#0f1419;--panel:#1a212b;--line:#2a3542;--txt:#d7e0ea;--dim:#8494a5;
--up:#3fb96a;--down:#e05260;--init:#e0a13f;--admin:#7a8aa0;--accent:#4aa3ff;}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--txt);
font-family:-apple-system,"PingFang SC","Microsoft YaHei",monospace;font-size:13px}
header{padding:14px 20px;background:var(--panel);border-bottom:1px solid var(--line);
display:flex;align-items:center;gap:16px;flex-wrap:wrap}
h1{font-size:16px;margin:0;font-weight:600}
.ctrl{display:flex;gap:8px;align-items:center;flex-wrap:wrap}
select,button,input{background:#0d1218;color:var(--txt);border:1px solid var(--line);
border-radius:6px;padding:6px 10px;font-size:13px}
button{cursor:pointer;background:var(--accent);color:#06121f;border-color:var(--accent);font-weight:600}
button.ghost{background:transparent;color:var(--txt);border-color:var(--line)}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px;padding:14px 20px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px;min-width:0}
.card h2{font-size:13px;margin:0 0 12px;color:var(--dim);font-weight:600;
text-transform:uppercase;letter-spacing:.5px}
.full{grid-column:1 / -1}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);white-space:nowrap}
th{color:var(--dim);font-weight:500}
td.num,th.num{text-align:right;font-variant-numeric:tabular-nums}
.badge{padding:2px 8px;border-radius:10px;font-size:11px;font-weight:600}
.s-Up{background:rgba(63,185,106,.18);color:var(--up)}
.s-Down{background:rgba(224,82,96,.18);color:var(--down)}
.s-Init{background:rgba(224,161,63,.18);color:var(--init)}
.s-AdminDown{background:rgba(122,138,160,.2);color:var(--admin)}
.fsm{display:flex;align-items:center;justify-content:center;gap:0;padding:10px}
.state{border:2px solid var(--line);border-radius:10px;padding:14px 20px;text-align:center;min-width:96px}
.state.active{border-color:var(--accent);box-shadow:0 0 14px rgba(74,163,255,.4)}
.arrow{color:var(--dim);padding:0 10px;font-size:18px}
.timeline{max-height:320px;overflow:auto;font-size:12px}
.tl-row{display:flex;gap:10px;padding:3px 4px;border-bottom:1px solid #202a35}
.tl-t{color:var(--dim);width:70px;flex:none;font-variant-numeric:tabular-nums}
.tl-node{width:78px;flex:none}
.tl-type{width:96px;flex:none;color:var(--accent)}
.tl-detail{color:var(--txt);word-break:break-all}
.pass{color:var(--up);font-weight:600}.fail{color:var(--down);font-weight:600}
.metric{display:inline-block;margin-right:18px;margin-bottom:8px}
.metric b{font-size:18px;display:block}
.switch-bar{display:flex;align-items:center;gap:12px;padding:8px 0;border-bottom:1px solid #202a35}
.iface{padding:6px 14px;border-radius:8px;border:1px solid var(--line)}
.iface.active{border-color:var(--up);background:rgba(63,185,106,.12);color:var(--up);font-weight:600}
.muted{color:var(--dim)}
</style>
</head>
<body>
<header>
  <h1>🚡 BFD 双向转发检测 · 缆车控制网仿真</h1>
  <div class="ctrl">
    <label>模式
      <select id="mode">
        <option value="async">异步模式</option>
        <option value="echo">回声模式</option>
        <option value="demand">按需模式</option>
      </select>
    </label>
    <label>故障
      <select id="fault">
        <option value="twoway">双向中断</option>
        <option value="oneway">单向中断</option>
        <option value="jitter">时延抖动</option>
        <option value="loss">丢包</option>
      </select>
    </label>
    <label>种子 <input id="seed" type="number" value="42" style="width:80px"></label>
    <button onclick="run()">运行仿真</button>
    <span id="summary" class="muted"></span>
  </div>
</header>

<div class="grid">
  <section class="card"><h2>① 会话列表</h2><div id="sessions"></div></section>
  <section class="card"><h2>② 状态机(三态转换表)</h2>
    <div class="fsm">
      <div class="state" id="st-Down">关闭<br><small>AdminDown/Down</small></div>
      <span class="arrow">→</span>
      <div class="state" id="st-Init">初始<br><small>Init</small></div>
      <span class="arrow">→</span>
      <div class="state" id="st-Up">建立<br><small>Up</small></div>
    </div>
    <div id="fsmnote" class="muted" style="padding:0 10px"></div>
  </section>

  <section class="card full"><h2>③ 收发时间线</h2>
    <div class="timeline" id="timeline"></div></section>

  <section class="card"><h2>④ 检测时间统计</h2><div id="detect"></div></section>
  <section class="card"><h2>⑤ 链路切换(上层流量保护)</h2><div id="switch"></div></section>
</div>

<script>
const $=id=>document.getElementById(id);
const ms=n=>(n/1e6).toFixed(1)+'ms';
async function run(){
  const body={mode:$('mode').value,fault:$('fault').value,
    seed:parseInt($('seed').value)||42};
  const r=await fetch('/api/run',{method:'POST',
    headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  const d=await r.json();
  render(d);
}
function render(d){
  // ① 会话列表
  $('sessions').innerHTML='<table><thead><tr><th>节点</th><th>链路</th>'+
   '<th>状态</th><th class="num">发包</th><th class="num">收包</th>'+
   '<th class="num">协商发送</th><th class="num">协商接收</th>'+
   '<th class="num">检测时间</th><th class="num">丢弃</th></tr></thead><tbody>'+
   d.sessions.map(s=>'<tr><td>'+s.node+'</td><td>'+s.linkId+'</td>'+
    '<td><span class="badge s-'+s.state+'">'+s.state+'</span></td>'+
    '<td class="num">'+s.txPackets+'</td><td class="num">'+s.rxPackets+'</td>'+
    '<td class="num">'+ms(s.txIntervalNs)+'</td><td class="num">'+ms(s.rxIntervalNs)+'</td>'+
    '<td class="num">'+ms(s.detectionTimeNs)+'</td>'+
    '<td class="num">'+Object.values(s.dropCounts).reduce((a,b)=>a+b,0)+'</td></tr>')
   .join('')+'</tbody></table>';
  // ② 状态机高亮(取主链路两端)
  ['Down','Init','Up'].forEach(k=>$('st-'+k).classList.remove('active'));
  const main=d.sessions.filter(s=>s.linkId==='primary');
  const states=[...new Set(main.map(s=>s.state))];
  states.forEach(k=>{const el=$('st-'+k);if(el)el.classList.add('active')});
  $('fsmnote').textContent='转换规则:Down+Init→Init,Init+Up→Up,Up+Down→Down。'+
   '主链路当前状态: '+main.map(s=>s.node+'='+s.state).join(', ');
  // ③ 时间线(最近 200 条,避免过长)
  const tl=d.timeline.slice(-200);
  $('timeline').innerHTML=tl.map(e=>'<div class="tl-row"><span class="tl-t">'+ms(e.t)+
    '</span><span class="tl-node">'+e.node+'</span><span class="tl-type">'+e.type+
    '</span><span class="tl-detail">'+e.detail+'</span></div>').join('');
  // ④ 检测统计
  const R=d.result;
  let h='<div><span class="metric"><b class="'+(R.passed?'pass':'fail')+'">'+
    (R.passed?'通过':'失败')+'</b>结论</span>'+
    '<span class="metric"><b>'+R.falseDowns+'</b>误判次数</span>'+
    '<span class="metric"><b>'+R.falseUps+'</b>误恢复</span></div>';
  h+='<table><thead><tr><th>节点</th><th>诊断</th><th class="num">实测检测</th>'+
    '<th class="num">理论下界</th><th class="num">理论上界</th><th>边界</th></tr></thead><tbody>';
  h+=R.records.map(x=>'<tr><td>'+x.node+'</td><td>'+x.diag+'</td>'+
    '<td class="num">'+ms(x.elapsed)+'</td><td class="num">'+ms(x.theoryMin)+
    '</td><td class="num">'+ms(x.theoryMax)+'</td>'+
    '<td>'+(x.withinBound?'<span class="pass">✓ 内</span>':'<span class="fail">✗ 越界</span>')+
    '</td></tr>').join('');
  h+='</tbody></table><p class="muted" style="margin-top:8px">'+
    (R.detail||'检测时间均落在按协商公式推导的理论范围内,同种子结果可复现。')+'</p>';
  $('detect').innerHTML=h;
  // ⑤ 链路切换
  let sh='<div class="switch-bar"><span class="muted">山顶当前:</span>'+
    ['primary','backup'].map(k=>'<span class="iface '+
      (d.activeLink[0]===k?'active':'')+'">'+(k==='primary'?'主用光纤':'备用光纤')+'</span>').join('')+
    '</div><div class="switch-bar"><span class="muted">山脚当前:</span>'+
    ['primary','backup'].map(k=>'<span class="iface '+
      (d.activeLink[1]===k?'active':'')+'">'+(k==='primary'?'主用光纤':'备用光纤')+'</span>').join('')+
    '</div>';
  sh+='<table style="margin-top:10px"><thead><tr><th class="num">时刻</th><th>节点</th>'+
    '<th>切换</th><th>原因</th></tr></thead><tbody>'+
    d.switches.map(e=>'<tr><td class="num">'+ms(e.t)+'</td><td>'+e.node+'</td><td>'+
      e.from+' → '+e.to+'</td><td>'+e.reason+'</td></tr>').join('')+'</tbody></table>';
  $('switch').innerHTML=sh;
  $('summary').textContent='种子'+d.spec.seed+' · '+d.spec.mode+' · '+d.spec.fault+
    ' · '+(R.passed?'零误判 ✓':'存在问题');
}
run();
</script>
</body>
</html>`
