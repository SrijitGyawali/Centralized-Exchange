// The whole web client, in plain JavaScript with no framework.
//
// Security rules followed throughout:
//   - text is only ever written with textContent, never innerHTML, so no
//     value from the server or a user can be interpreted as HTML;
//   - there is no inline script, so the server's Content-Security-Policy can
//     forbid inline scripts entirely;
//   - the API token lives in sessionStorage only (gone when the tab closes)
//     and is sent only as an Authorization header to this same server.
'use strict';

const $ = (id) => document.getElementById(id);
let token = sessionStorage.getItem('cex.token') || '';
let lastOrder = null; // { key, body }: to demonstrate a retry with the same key

// ---------------------------------------------------------------- market data
// One WebSocket to /stream/book. Every message is a COMPLETE snapshot, so a
// reconnect needs no special handling: the first message redraws everything.
let backoff = 500;
function connect() {
  const scheme = location.protocol === 'https:' ? 'wss://' : 'ws://';
  const ws = new WebSocket(scheme + location.host + '/stream/book');
  ws.onopen = () => { backoff = 500; setConn('live', true); };
  ws.onmessage = (e) => render(JSON.parse(e.data));
  ws.onclose = (e) => {
    // The server says why: "slow consumer…" or "server shutting down".
    setConn('reconnecting' + (e.reason ? ' (' + e.reason + ')' : ''), false);
    setTimeout(connect, backoff);
    backoff = Math.min(backoff * 2, 10000);
  };
}

function setConn(text, ok) {
  $('conn').textContent = text;
  $('conn').className = 'conn ' + (ok ? 'ok' : 'bad');
}

function cells(tr, values, classes) {
  values.forEach((v, i) => {
    const td = document.createElement('td');
    td.textContent = v;
    if (classes && classes[i]) td.className = classes[i];
    tr.appendChild(td);
  });
  return tr;
}

function fill(tbody, rows) {
  tbody.replaceChildren(...rows.map((r) => cells(document.createElement('tr'), r.values, r.classes)));
}

function render(m) {
  $('seq').textContent = m.seq;
  // Asks are sent best (lowest) first; show them highest first, so the best
  // ask sits right above the spread, as on every trading screen.
  fill($('asks'), m.asks.slice().reverse().map((l) => ({ values: [l.price, l.quantity, l.orders] })));
  fill($('bids'), m.bids.map((l) => ({ values: [l.price, l.quantity, l.orders] })));
  $('spread').textContent = (m.bids.length && m.asks.length)
    ? 'spread ' + (BigInt(m.asks[0].price) - BigInt(m.bids[0].price)) // exact integer arithmetic
    : 'no spread (a side is empty)';
  fill($('trades'), m.trades.slice().reverse().map((t) => ({
    values: [t.seq, t.price, t.quantity, t.side], classes: ['', '', '', t.side],
  })));
}

// --------------------------------------------------------------- private API
async function api(method, path, body, key) {
  const headers = { Authorization: 'Bearer ' + token };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (key) headers['Idempotency-Key'] = key;
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  return { status: res.status, data };
}

async function refresh() {
  if (!token) return;
  const s = await api('GET', '/v1/status');
  if (s.status === 401) { forget('token rejected'); return; }
  if (s.data) {
    $('applied').textContent = s.data.applied_seq;
    const p = s.data.projection || {};
    $('projected').textContent = p.enabled ? p.projected_seq : 'read model off';
    $('lag').textContent = p.enabled ? p.lag + (p.healthy ? '' : ' (database unavailable)') : '–';
  }
  const b = await api('GET', '/v1/balances');
  if (b.data && b.data.balances) {
    fill($('balances'), b.data.balances.map((r) => ({ values: [r.asset, r.available, r.reserved] })));
  }
}

function showResult(r, key) {
  const d = r.data || {};
  const lines = ['HTTP ' + r.status + (key ? '   Idempotency-Key ' + key : '')];
  if (d.error) lines.push('error: ' + d.error + (d.detail ? ' – ' + d.detail : ''));
  if (d.order_id && d.order_id !== '0') lines.push('order ' + d.order_id + ': ' + (d.outcome || d.rejected));
  if (d.rejected && d.rejected !== 'accepted') lines.push('rejected: ' + d.rejected + (d.detail ? ' – ' + d.detail : ''));
  (d.fills || []).forEach((f) => lines.push('  fill ' + f.quantity + ' BTC @ ' + f.price));
  if (d.duplicate) lines.push('duplicate: this is the ORIGINAL result, nothing ran twice');
  $('result').textContent = lines.join('\n');
}

async function placeOrder(e) {
  e.preventDefault();
  const body = {
    symbol: 'BTC-USDT', type: 'limit', side: $('side').value, time_in_force: $('tif').value,
    price: $('price').value.trim(), quantity: $('qty').value.trim(),
  };
  lastOrder = { key: crypto.randomUUID(), body };
  showResult(await api('POST', '/v1/orders', body, lastOrder.key), lastOrder.key);
  $('retry').hidden = false;
  refresh();
}

async function retry() {
  if (!lastOrder) return;
  // Same key, same body: the server returns the stored result, marked duplicate.
  showResult(await api('POST', '/v1/orders', lastOrder.body, lastOrder.key), lastOrder.key);
  refresh();
}

function useToken(e) {
  e.preventDefault();
  token = $('token').value.trim();
  $('token').value = '';
  if (!token) return;
  sessionStorage.setItem('cex.token', token);
  $('login').hidden = true;
  $('account').hidden = false;
  refresh();
}

function forget(reason) {
  token = '';
  sessionStorage.removeItem('cex.token');
  $('login').hidden = false;
  $('account').hidden = true;
  if (reason) $('token').placeholder = reason;
}

document.addEventListener('DOMContentLoaded', () => {
  $('login').addEventListener('submit', useToken);
  $('order').addEventListener('submit', placeOrder);
  $('retry').addEventListener('click', retry);
  $('logout').addEventListener('click', () => forget(''));
  if (token) { $('login').hidden = true; $('account').hidden = false; refresh(); }
  setInterval(refresh, 2000);
  connect();
});
