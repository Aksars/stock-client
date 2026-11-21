"""Execution of the server's decision on the user's account (executor-v1; ported from broker-exec-v5 of the server, user's decision
21.11.2026: the client keeps the token and trades by itself, the server gives only the target weights).

Modes (data/config.json): 'off' — nothing; 'sandbox' — T-Invest sandbox account of the trading token (virtual money, paid in the value
of the configured real account; later deposits to the real account are mirrored); 'live' — real orders to ONE account, config
'account_id', only if the token has full access to it. Only the user switches to 'live' (control.py: fresh check + typed phrase).
Guards: a decision is executed once (by decision_id); not after its valid_until; new money (cash > 3% of the portfolio) is invested
by the last executed decision for up to 8 days after its date; the stop flag halts everything, also between orders; live: no new
orders while the account has active ones, one order at most max_order_rub; nothing is posted while the market is closed."""
import datetime as dt, time, uuid
from . import config as C, planner as P, server as S, tinvest as T

POLICY = 'executor-v1'
TOPUP, TOPUP_AGE = 0.03, 8


def state_name(live):
    return 'state_live.json' if live else 'state_sandbox.json'


def live_account(cfg):
    seen = T.accounts()
    pick = [a for a in seen if a['id'] == cfg.get('account_id')] if cfg.get('account_id') else []
    ok = len(pick) == 1 and pick[0]['status'] == 'ACCOUNT_STATUS_OPEN' and pick[0]['access'] == 'ACCOUNT_ACCESS_LEVEL_FULL_ACCESS'
    return (pick[0] if ok else None), seen


def setup_sandbox(cfg):
    """Open the sandbox account once and pay in the value of the configured real account (read only)."""
    st = C.load('state_sandbox.json', {}) or {}
    if st.get('account'):
        return st
    a, _ = live_account(cfg)
    if not a:
        raise RuntimeError('нет настроенного счёта с полным доступом: не знаю, сколько внести в песочницу')
    _, _, total = T.portfolio(a['id'], sandbox=False)
    amount = round(total or 1_000_000.0, 2)
    acc = T.call('SandboxService/OpenSandboxAccount', {'name': 'stock-client — проверка'})['accountId']
    T.call('SandboxService/SandboxPayIn', {'accountId': acc, 'amount': T.money(amount)})
    st = {'account': acc, 'opened': dt.date.today().isoformat(), 'paid_in': amount, 'mirrored_in': 0.0, 'policy': POLICY}
    C.save('state_sandbox.json', st)
    return st


def mirror_inputs(cfg, st):
    """Deposits to the real account since the sandbox was opened are paid into the sandbox in the same amount."""
    now = dt.datetime.now(dt.timezone.utc)
    ops = T.call('OperationsService/GetOperations', {'accountId': cfg['account_id'], 'from': st['opened'] + 'T00:00:00Z',
                                                     'to': now.strftime('%Y-%m-%dT%H:%M:%SZ'), 'state': 'OPERATION_STATE_EXECUTED'}).get('operations', [])
    inp = sum(T.q(o.get('payment')) or 0 for o in ops if o.get('operationType') == 'OPERATION_TYPE_INPUT')
    add = round(inp - float(st.get('mirrored_in', 0.0)), 2)
    if add >= 1:
        T.call('SandboxService/SandboxPayIn', {'accountId': st['account'], 'amount': T.money(add)})
        st['mirrored_in'] = round(inp, 2)
        st.setdefault('payins', []).append({'at': now.isoformat(), 'amount': add})
        C.save('state_sandbox.json', st)
    return add


def market_info(tickers, pos):
    info, skip = {}, []
    for t in sorted(tickers):
        try:
            ins = T.instrument(t)
            px = T.last_price(ins['uid'])
        except T.Unavailable as e:
            skip.append(f'{t}: {e}')
            continue
        if not px:
            skip.append(f'{t}: нет цены')
            continue
        info[t] = {'uid': ins['uid'], 'lot': ins['lot'], 'step': ins['step'], 'px': px, 'have': pos.get(t, {}).get('qty', 0.0)}
    return info, skip


def pick_decision(st, cash, total, today):
    """(decision, topup, note): a new decision from the server, or the last executed one for new money."""
    topup = bool(st.get('last_decision')) and total > 0 and cash / total > TOPUP
    try:
        d = S.fetch()
    except S.Bad as e:
        d, note = S.cached(), f'сервер: {e}'
        if not d or not topup or d.get('decision_id') != st.get('last_decision'):
            return None, topup, note + ' — ничего не отправлено'
    if d['decision_id'] == st.get('last_decision'):
        if not topup:
            return None, False, f"решение {d['decision_date']} уже исполнено"
        age = (dt.date.fromisoformat(today) - dt.date.fromisoformat(d['decision_date'])).days
        if age > TOPUP_AGE:
            return None, True, f"решение {d['decision_date']} старше {TOPUP_AGE} дней — новые деньги ждут следующего"
        return d, True, None
    if S.expired(d, today):
        return None, topup, f"решение {d['decision_date']} действовало до {d['valid_until']} — ничего не отправлено"
    return d, topup, None


def build(cfg, live, today=None, dry=False):
    """Everything up to the orders. Returns a dict with 'account', 'decision', 'orders', 'skipped' or 'result' (a reason to stop)."""
    today = today or dt.date.today().isoformat()
    if live:
        a, seen = live_account(cfg)
        if not a:
            return {'result': f'реальный счёт: токен видит {len(seen)} счёт(а), настроенного открытого с полным доступом среди них нет', 'accounts_seen': seen}
        acc, st = a['id'], C.load('state_live.json', {}) or {}
        if not dry and T.call('OrdersService/GetOrders', {'accountId': acc}).get('orders'):
            return {'result': 'на счёте есть активные заявки — новых не отправляю'}
    else:
        st = setup_sandbox(cfg)
        acc = st['account']
        try:
            mirror_inputs(cfg, st)
        except Exception as e:
            st.setdefault('notes', []).append(f'mirror: {type(e).__name__}')
    pos, cash, total = T.portfolio(acc, sandbox=not live)
    if dry:
        st = {**st, 'last_decision': None}
    d, topup, note = pick_decision(st, cash, total, today)
    if not d:
        return {'result': note, 'account': acc}
    target, ct = d['weights'], d['cash_ticker']
    ours = set(target) | set(st.get('traded', [])) | {ct}   # a stock that left the target is sold; other holdings are not touched
    info, skip = market_info({t for t in ours if t in target or t in pos}, pos)
    skip += [f'{t}: не из стратегии — не трогаю' for t in pos if t not in ours]
    orders, s2 = P.plan(target, info, cash, total, ct, has_positions=bool(set(pos) & ours))
    skip += s2
    if live:
        cap = float(cfg.get('max_order_rub') or 100_000)
        for o in orders:
            if o['value'] > cap:
                n = int(cap / (o['price'] * o['lot']))
                skip.append(f"{o['ticker']}: заявка урезана до {n} лот(ов) — предел {cap:,.0f} ₽".replace(',', ' '))
                o['lots'], o['value'] = n, round(n * o['lot'] * o['price'], 2)
        orders = [o for o in orders if o['lots'] > 0]
    return {'account': acc, 'state': st, 'decision': d, 'topup': topup, 'target': target, 'orders': orders, 'skipped': skip,
            'positions': {t: v['qty'] for t, v in pos.items()}, 'cash': round(cash, 2), 'total': total}


def run(today=None):
    """One execution pass (scheduler or the interface). One pass at a time."""
    try:
        with C.Lock('exec.lock', nowait=True):
            return _run(today)
    except BlockingIOError:
        return 'уже идёт другой проход'


def _run(today=None):
    cfg = C.config()
    if cfg['mode'] not in ('sandbox', 'live') or cfg.get('stop'):
        return f"режим {cfg['mode']}{' (стоп)' if cfg.get('stop') else ''}: ничего не отправлено"
    if not T.has_token():
        return 'нет торгового токена: ничего не отправлено'
    live = cfg['mode'] == 'live'
    b = build(cfg, live, today)
    if 'result' in b:
        return b['result']
    orders, skip, sent = b['orders'], b['skipped'], []
    if orders:
        ts = T.call('MarketDataService/GetTradingStatus', {'instrumentId': orders[0]['uid']})
        if not ts.get('limitOrderAvailableFlag'):
            return f"биржа закрыта для {orders[0]['ticker']} ({ts.get('tradingStatus')}): {len(orders)} заявок ждут"
    for o in orders:
        time.sleep(1.5)   # the broker limits the request rate
        c = C.config()
        if c.get('stop') or c.get('mode') != cfg['mode']:
            skip.append('остановлено с интерфейса — остальные заявки не отправлены')
            break
        oid = str(uuid.uuid4())
        body = {'accountId': b['account'], 'instrumentId': o['uid'], 'quantity': str(o['lots']), 'price': T.price(o['price']),
                'direction': 'ORDER_DIRECTION_BUY' if o['side'] == 'buy' else 'ORDER_DIRECTION_SELL', 'orderType': 'ORDER_TYPE_LIMIT', 'orderId': oid}
        try:
            r = T.call('OrdersService/PostOrder' if live else 'SandboxService/PostSandboxOrder', body)
            sent.append({**o, 'order_id': oid, 'status': r.get('executionReportStatus'), 'filled_lots': r.get('lotsExecuted')})
        except T.Unavailable as e:
            sent.append({**o, 'order_id': oid, 'status': 'error', 'error': str(e)[:160]})
    d = b['decision']
    log = C.load('exec_log.json', []) or []
    log.append({'at': dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds'), 'mode': cfg['mode'], 'account': b['account'], 'policy': POLICY,
                'decision': d['decision_date'], 'decision_id': d['decision_id'], 'topup': b['topup'], 'target': b['target'],
                'before': {'positions': b['positions'], 'cash': b['cash'], 'total': b['total']}, 'orders': sent, 'skipped': skip})
    C.save('exec_log.json', log[-500:])
    blocked = any('ничего не отправлено' in x for x in skip)
    if any(o.get('status') != 'error' for o in sent) or (not orders and not blocked):   # a blocked run is retried next time
        st = C.load(state_name(live), {}) or b['state']
        st['last_decision'] = d['decision_id']
        st['traded'] = sorted(set(st.get('traded', [])) | {t for t, w in d['weights'].items() if w > 0})
        C.save(state_name(live), st)
    return f"{cfg['mode']}: {len(sent)} заявок по решению {d['decision_date']}, пропусков {len(skip)}"


def dry_run():
    """Read the real account and show the orders the current decision would post there. Posts nothing."""
    cfg = C.config()
    b = build(cfg, live=True, dry=True)
    b.pop('state', None)
    return b
