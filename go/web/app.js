'use strict';
const $ = (id) => document.getElementById(id);
const rub = (x) => x == null ? '—' : Math.round(x).toLocaleString('ru-RU') + ' ₽';
const pct = (x) => (100 * x).toLocaleString('ru-RU', { maximumFractionDigits: 2 }) + '%';
const esc = (s) => String(s ?? '').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
const when = (s) => s ? new Date(s).toLocaleString('ru-RU', { dateStyle: 'short', timeStyle: 'short' }) : '—';
const MODES = { off: 'выключено', sandbox: 'песочница', live: 'РЕАЛЬНЫЙ СЧЁТ' };
let S = null;

async function api(path, body) {
  const r = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Stock-Client': '1' }, body: JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.detail || ('HTTP ' + r.status));
  return j;
}

function ordersTable(orders) {
  if (!orders || !orders.length) return '<div class="muted">заявок нет</div>';
  return '<table><tr><th>Бумага</th><th>Сторона</th><th class="n">Лотов</th><th class="n">Цена</th><th class="n">Сумма</th><th>Статус</th></tr>' +
    orders.map((o) => `<tr><td>${esc(o.ticker)}</td><td class="${o.side === 'buy' ? 'ok' : 'warn'}">${o.side === 'buy' ? 'купить' : 'продать'}</td>` +
      `<td class="n">${o.lots}</td><td class="n">${o.price}</td><td class="n">${rub(o.value)}</td><td>${esc(o.status || '')}${o.error ? ' <span class="bad">' + esc(o.error) + '</span>' : ''}</td></tr>`).join('') + '</table>';
}

function list(items) { return items && items.length ? '<ul>' + items.map((x) => `<li>${esc(x)}</li>`).join('') + '</ul>' : ''; }

function render(s) {
  S = s;
  const m = $('mode'); m.textContent = MODES[s.mode] || s.mode; m.className = 'pill ' + s.mode;
  $('stopPill').hidden = !s.stop; $('stop').hidden = s.stop; $('unstop').hidden = !s.stop;
  $('window').textContent = s.window;
  document.querySelectorAll('[data-mode]').forEach((b) => { b.disabled = b.dataset.mode === s.mode; });
  if (document.activeElement !== $('limit')) $('limit').value = s.max_order_rub;
  $('runNow').disabled = s.mode !== 'sandbox';

  const d = s.decision;
  if (!d) $('decision').innerHTML = 'Решение ещё не получено.';
  else {
    const w = Object.entries(d.weights).sort((a, b) => b[1] - a[1]);
    const today = new Date().toISOString().slice(0, 10);
    const exp = today > d.valid_until;
    $('decision').innerHTML = `<div class="row"><b>от ${esc(d.decision_date)}</b><span class="${exp ? 'bad' : 'ok'}">действует до ${esc(d.valid_until)}</span>` +
      `<span class="muted">получено ${when(d.fetched_at)}</span></div><table>` +
      w.map(([t, x]) => `<tr><td style="width:70px">${esc(t)}${t === d.cash_ticker ? ' <span class="muted">фонд</span>' : ''}</td><td class="n" style="width:80px">${pct(x)}</td>` +
        `<td><div class="bar" style="width:${(100 * x).toFixed(1)}%"></div></td></tr>`).join('') + '</table>' +
      `<div class="muted">${esc(d.policy)} · ${esc(d.decision_id)} · подпись сервера проверена · исполнять: закрытие следующей сессии</div>`;
  }

  const facts = [
    s.trade_token ? '<span class="ok">торговый токен на месте</span>' : '<span class="bad">нет торгового токена (secrets/tinvest-trade.txt)</span>',
    s.server_key ? 'ключ сервера на месте' : '<span class="bad">нет ключа сервера</span>',
    'счёт: ' + esc(s.account_id || 'не задан'),
    s.sandbox.account ? `песочница открыта ${esc(s.sandbox.opened)}, внесено ${rub(s.sandbox.paid_in)}` : 'песочница ещё не открыта',
    s.last_pass ? `последний проход ${when(s.last_pass.at)}: ${esc(s.last_pass.result)}` : 'проходов ещё не было',
  ];
  $('facts').innerHTML = facts.join('<br>');
  $('tokenState').innerHTML = s.trade_token ? '<span class="ok">Токен сохранён.</span> Вставьте новый, чтобы заменить.' : '<span class="bad">Токена нет.</span>';

  $('auto').checked = !!s.auto_execute;
  if (document.activeElement !== $('serverUrl')) $('serverUrl').value = s.server_url || '';
  $('serverKey').placeholder = s.server_key ? 'сохранён — вставьте новый, чтобы заменить' : 'выдаёт владелец сервера';
  if (s.account_id && ![...$('account').options].some((o) => o.value === s.account_id)) $('account').add(new Option('счёт ' + s.account_id, s.account_id));
  if (s.account_id) $('account').value = s.account_id;
  const p = s.pending;
  if (p && p.decision) {
    $('pending').innerHTML = `<div class="row"><b>решение ${esc(p.decision.decision_date)}</b><span class="muted">рассчитан ${when(p.built_at)} · ${MODES[p.mode] || p.mode}${p.topup ? ' · новые деньги' : ''} · портфель ${rub(p.total)}</span></div>` +
      ordersTable(p.orders) + list(p.skipped);
    $('confirm').hidden = !(p.orders && p.orders.length); $('confirm').dataset.id = p.decision.decision_id;
  } else { $('pending').textContent = 'Плана нет.'; $('confirm').hidden = true; }
  if (s.holdings && s.holdings.length) {
    $('holdings').innerHTML = '<table><tr><th>Бумага</th><th class="n">Кол-во</th><th>Состояние</th><th>Отдать стратегии</th></tr>' + s.holdings.map((h) => {
      const fixed = h.in_decision || h.bought_by_client;
      const st = h.in_decision ? 'в решении' : h.bought_by_client ? 'куплена клиентом' : h.handed_over ? 'отдана стратегии' : 'не трогается';
      return `<tr><td>${esc(h.ticker)}</td><td class="n">${h.qty}</td><td>${st}</td><td>${fixed ? '—' : `<input type="checkbox" data-t="${esc(h.ticker)}" ${h.handed_over ? 'checked' : ''}>`}</td></tr>`;
    }).join('') + '</table>';
    $('managedSave').hidden = false;
  }
  const c = s.check;
  if (c && c.at) {
    let h = `<div class="row"><b class="${c.ok ? 'ok' : 'bad'}">${c.ok ? 'счёт доступен' : 'не прошла'}</b><span class="muted">${when(c.at)}${s.check_fresh ? ' · свежая' : ' · устарела'}</span></div>`;
    if (c.reason) h += `<div class="bad">${esc(c.reason)}</div>`;
    if (c.plan && !c.reason) h += `<div class="muted">решение ${esc((c.plan.decision || {}).decision_date)} · портфель ${rub(c.plan.total)} · деньги ${rub(c.plan.cash)}</div>` + ordersTable(c.plan.orders) + list(c.plan.skipped);
    $('checkBox').innerHTML = h;
  }

  $('runs').innerHTML = s.runs.length ? s.runs.map((r) => `<div class="run"><div class="row"><b>${when(r.at)}</b><span class="pill ${r.mode}">${MODES[r.mode]}</span>` +
    `<span class="muted">решение ${esc(r.decision)}${r.topup ? ' · новые деньги' : ''} · портфель ${rub((r.before || {}).total)}</span></div>${ordersTable(r.orders)}${list(r.skipped)}</div>`).join('') : 'проходов с заявками ещё не было';
  $('history').innerHTML = s.history.length ? '<table>' + s.history.map((h) => `<tr><td>${when(h.at)}</td><td>${esc(h.what)}</td><td>${esc(JSON.stringify(h.from))} → ${esc(JSON.stringify(h.to))}</td></tr>`).join('') + '</table>' : 'изменений нет';
}

async function act(msgId, fn) {
  $(msgId).textContent = '…'; $(msgId).className = 'msg';
  try { const r = await fn(); render(r); $(msgId).textContent = r.result || 'готово'; $(msgId).className = 'msg ok'; }
  catch (e) { $(msgId).textContent = e.message; $(msgId).className = 'msg bad'; }
}

document.querySelectorAll('[data-mode]').forEach((b) => b.addEventListener('click', () => {
  if (b.dataset.mode === 'live') { $('liveBox').hidden = false; $('phrase').placeholder = 'введите «' + S.phrase + '»'; $('phrase').focus(); return; }
  act('ctlMsg', () => api('/api/mode', { mode: b.dataset.mode }));
}));
$('liveGo').addEventListener('click', () => act('ctlMsg', () => api('/api/mode', { mode: 'live', confirm: $('phrase').value })));
$('stop').addEventListener('click', () => act('ctlMsg', () => api('/api/stop', { stop: true })));
$('unstop').addEventListener('click', () => {
  const confirm = S.mode === 'live' ? prompt('Реальный счёт: введите «' + S.phrase + '», чтобы снять остановку') : null;
  act('ctlMsg', () => api('/api/stop', { stop: false, confirm }));
});
$('limitSave').addEventListener('click', () => act('ctlMsg', () => api('/api/limit', { max_order_rub: Number($('limit').value) })));
$('runNow').addEventListener('click', () => act('ctlMsg', () => api('/api/run', {})));
$('refresh').addEventListener('click', () => act('decMsg', () => api('/api/decision/refresh', {})));
$('auto').addEventListener('change', () => {
  const on = $('auto').checked;
  const confirm = on && S.mode === 'live' ? prompt('Реальный счёт: введите «' + S.phrase + '», чтобы заявки уходили без вашего подтверждения') : '';
  act('ctlMsg', () => api('/api/auto', { auto: on, confirm }));
});
$('planNow').addEventListener('click', () => act('planMsg', () => api('/api/plan', {})));
$('confirm').addEventListener('click', () => act('planMsg', () => api('/api/confirm', { decision_id: $('confirm').dataset.id })));
$('managedSave').addEventListener('click', () => act('manMsg', () => api('/api/managed', { tickers: [...document.querySelectorAll('#holdings input[data-t]:checked')].map((x) => x.dataset.t) })));
$('serverSave').addEventListener('click', () => act('setMsg', () => api('/api/settings', { server_url: $('serverUrl').value })));
$('keySave').addEventListener('click', () => { const key = $('serverKey').value; $('serverKey').value = ''; act('setMsg', () => api('/api/server-key', { key })); });
$('loginGo').addEventListener('click', () => {
  const b = { login: $('login').value, password: $('password').value, device: $('device').value || 'компьютер' };
  $('password').value = '';
  act('setMsg', () => api('/api/login', b));
});
$('logoutGo').addEventListener('click', () => act('setMsg', () => api('/api/logout', {})));
$('accLoad').addEventListener('click', async () => {
  $('setMsg').textContent = '…';
  try {
    const r = await api('/api/accounts', {});
    const sel = $('account'); sel.innerHTML = '';
    r.accounts.forEach((a) => { const o = new Option(`${a.name || 'счёт'} · ${a.id}${a.tradable ? '' : ' (нет права торговли)'}`, a.id); o.disabled = !a.tradable; sel.add(o); });
    if (S && S.account_id) sel.value = S.account_id;
    $('setMsg').textContent = 'счетов: ' + r.accounts.length; $('setMsg').className = 'msg ok';
  } catch (e) { $('setMsg').textContent = e.message; $('setMsg').className = 'msg bad'; }
});
$('accSave').addEventListener('click', () => act('setMsg', () => api('/api/settings', { account_id: $('account').value })));
$('tokenSave').addEventListener('click', () => {
  const token = $('token').value; $('token').value = '';
  act('tokMsg', () => api('/api/token', { token }));
});
$('check').addEventListener('click', () => act('chkMsg', () => api('/api/check', {})));

async function tick() { try { render(await api('/api/status')); } catch (e) { $('decision').textContent = 'клиент недоступен: ' + e.message; } }
tick(); setInterval(tick, 15000);
