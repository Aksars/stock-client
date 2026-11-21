"""The client's web app (stock-client-v1): control of the execution and the view of the decision, on this machine only.
Run: .venv/bin/uvicorn client.app:app --host 0.0.0.0 --port 8240   (outside: http://localhost:8240)
Ported from exec-control-v1 of the server: modes, emergency stop, order limit, account check without orders, switch to the real
account only after a fresh check (<= 30 min) and the typed phrase РЕАЛЬНЫЙ СЧЁТ. Every change goes to data/control.json.
Scheduler: weekdays 10:05-18:30 Moscow time, one pass every 5 minutes (CLIENT_SCHEDULER=0 switches it off)."""
import datetime as dt, os, threading, time
from pathlib import Path
from typing import Optional
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import FileResponse, JSONResponse
from pydantic import BaseModel
from . import config as C, executor as E, server as S, tinvest as T

VERSION = 'stock-client-v1'
PHRASE, CHECK_MAX_MIN = 'РЕАЛЬНЫЙ СЧЁТ', 30
MSK = dt.timezone(dt.timedelta(hours=3))
WEB = Path(__file__).resolve().parent / 'web'
app = FastAPI(title='Stock client', version=VERSION, docs_url=None, redoc_url=None, openapi_url=None)


@app.middleware('http')
async def guard(request: Request, call_next):
    if request.method not in ('GET', 'HEAD'):
        origin = request.headers.get('origin')
        if (origin and origin.split('://', 1)[-1] != request.headers.get('host')) or request.headers.get('sec-fetch-site') == 'cross-site':
            return JSONResponse({'detail': 'Запрос должен исходить из приложения'}, status_code=403)
    r = await call_next(request)
    r.headers['Cache-Control'] = 'no-store'
    r.headers['X-Content-Type-Options'] = 'nosniff'
    r.headers['X-Frame-Options'] = 'DENY'
    r.headers['Content-Security-Policy'] = "default-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
    return r


def _now():
    return dt.datetime.now(dt.timezone.utc)


def _change(what, before, after):
    c = C.load('control.json', {}) or {}
    c.setdefault('history', []).append({'at': _now().isoformat(timespec='seconds'), 'what': what, 'from': before, 'to': after})
    c['history'] = c['history'][-200:]
    C.save('control.json', c)


def _write_cfg(cfg):
    C.save('config.json', {k: cfg[k] for k in C.DEFAULTS})


def _orders(o):
    return [{k: x.get(k) for k in ('ticker', 'side', 'lots', 'lot', 'price', 'value', 'status', 'filled_lots', 'error')} for x in o or []]


def status():
    cfg = C.config()
    c = C.load('control.json', {}) or {}
    ck = c.get('check') or {}
    fresh = bool(ck.get('ok')) and ck.get('account') == cfg.get('account_id') and ck.get('at') and \
        (_now() - dt.datetime.fromisoformat(ck['at'])).total_seconds() < CHECK_MAX_MIN * 60
    log = C.load('exec_log.json', []) or []
    runs = [{'at': e['at'], 'mode': e['mode'], 'decision': e['decision'], 'topup': e.get('topup'), 'total': (e.get('before') or {}).get('total'),
             'orders': _orders(e.get('orders')), 'skipped': e.get('skipped') or []} for e in log[-15:]][::-1]
    sb, lv = C.load('state_sandbox.json', {}) or {}, C.load('state_live.json', {}) or {}
    return {'version': VERSION, 'executor': E.POLICY, 'mode': cfg['mode'], 'stop': bool(cfg.get('stop')), 'account_id': cfg.get('account_id'),
            'max_order_rub': float(cfg.get('max_order_rub') or 0), 'server_url': cfg['server_url'], 'trade_token': T.has_token(),
            'server_key': C.SERVER_KEY.is_file(), 'decision': S.cached(),
            'sandbox': {k: sb.get(k) for k in ('account', 'opened', 'paid_in', 'mirrored_in', 'last_decision')},
            'live': {'last_decision': lv.get('last_decision')},
            'check': {**{k: ck.get(k) for k in ('at', 'ok', 'account', 'reason', 'plan')}, 'fresh': bool(fresh)},
            'phrase': PHRASE, 'check_max_min': CHECK_MAX_MIN, 'runs': runs, 'history': (c.get('history') or [])[-20:][::-1],
            'last_pass': c.get('last_pass'), 'window': 'будни, 10:05–18:30 МСК, проход каждые 5 минут; одно решение исполняется один раз'}


@app.get('/api/status')
def get_status():
    return status()


@app.post('/api/decision/refresh')
def refresh():
    try:
        S.fetch()
    except S.Bad as e:
        raise HTTPException(502, str(e))
    return status()


@app.post('/api/check')
def check():
    """Read the real account and build the orders of the current decision for it. Posts nothing."""
    cfg = C.config()
    if not T.has_token():
        raise HTTPException(409, 'Торговый токен не найден: положите его в secrets/tinvest-trade.txt')
    try:
        r = E.dry_run()
        ok = 'orders' in r
        res = {'at': _now().isoformat(), 'ok': ok, 'account': r.get('account') or cfg.get('account_id'), 'reason': None if ok else r.get('result'),
               'plan': {'decision': (r.get('decision') or {}).get('decision_date'), 'target': r.get('target'), 'positions': r.get('positions'),
                        'cash': r.get('cash'), 'total': r.get('total'), 'orders': _orders(r.get('orders')), 'skipped': r.get('skipped')} if ok else None}
    except Exception as x:
        res = {'at': _now().isoformat(), 'ok': False, 'account': cfg.get('account_id'), 'reason': f'{type(x).__name__}: {str(x)[:160]}'}
    with C.Lock('config.lock'):
        c = C.load('control.json', {}) or {}
        c['check'] = res
        C.save('control.json', c)
    return status()


class Mode(BaseModel):
    mode: str
    confirm: Optional[str] = None


@app.post('/api/mode')
def set_mode(body: Mode):
    if body.mode not in ('off', 'sandbox', 'live'):
        raise HTTPException(422, 'Режим: off, sandbox или live')
    with C.Lock('config.lock'):
        cfg = C.config()
        before = cfg['mode']
        if body.mode == 'live':
            if (body.confirm or '').strip().upper() != PHRASE:
                raise HTTPException(409, f'Чтобы включить реальный счёт, введите фразу «{PHRASE}»')
            if not status()['check']['fresh']:
                raise HTTPException(409, f'Сначала проверьте счёт (не раньше чем за {CHECK_MAX_MIN} минут до включения)')
        if body.mode != 'off' and not cfg.get('account_id'):
            raise HTTPException(409, 'В настройке нет номера счёта')
        cfg['mode'] = body.mode
        _write_cfg(cfg)
    _change('mode', before, body.mode)
    return status()


class Stop(BaseModel):
    stop: bool
    confirm: Optional[str] = None


@app.post('/api/stop')
def set_stop(body: Stop):
    with C.Lock('config.lock'):
        cfg = C.config()
        before = bool(cfg.get('stop'))
        if not body.stop and cfg['mode'] == 'live' and (body.confirm or '').strip().upper() != PHRASE:
            raise HTTPException(409, f'Реальный счёт: чтобы снять остановку, введите фразу «{PHRASE}»')
        cfg['stop'] = bool(body.stop)
        _write_cfg(cfg)
    _change('stop', before, bool(body.stop))
    return status()


class Limit(BaseModel):
    max_order_rub: float


@app.post('/api/limit')
def set_limit(body: Limit):
    if not 1000 <= body.max_order_rub <= 1_000_000:
        raise HTTPException(422, 'Предел одной заявки: от 1 000 до 1 000 000 ₽')
    with C.Lock('config.lock'):
        cfg = C.config()
        before = cfg.get('max_order_rub')
        cfg['max_order_rub'] = round(float(body.max_order_rub))
        _write_cfg(cfg)
    _change('max_order_rub', before, cfg['max_order_rub'])
    return status()


class Token(BaseModel):
    token: str


TOKEN_RE = __import__('re').compile(r'[A-Za-z0-9._\-]{20,300}')


@app.post('/api/token')
def set_token(body: Token):
    """Paste a T-Invest token in the interface: it is checked with the broker (UsersService/GetAccounts) and only then saved to
    secrets/tinvest-trade.txt. The token is never returned, logged or written anywhere else. Replacing it is allowed only while the
    executor is off or stopped, so a pass never runs half on the old token and half on the new one."""
    t = body.token.strip()
    if not TOKEN_RE.fullmatch(t):
        raise HTTPException(422, 'Это не похоже на токен T-Invest (латинские буквы, цифры, «.», «-», «_», от 20 символов)')
    cfg = C.config()
    if cfg['mode'] != 'off' and not cfg.get('stop'):
        raise HTTPException(409, 'Сначала выключите исполнение или нажмите «Аварийная остановка»')
    try:
        seen = T.accounts(token=t)
    except T.Unavailable as e:
        raise HTTPException(400, f'Брокер не принял токен: {e}')
    with C.Lock('exec.lock'):
        T.save_token(t)
    acc = next((a for a in seen if a['id'] == cfg.get('account_id')), None)
    note = ('полный доступ к счёту ' + acc['id']) if acc and acc['access'] == 'ACCOUNT_ACCESS_LEVEL_FULL_ACCESS' else \
        ('к счёту ' + str(cfg.get('account_id')) + ' нет полного доступа — торговать им нельзя' if cfg.get('account_id') else 'номер счёта в настройке не задан')
    _change('token', None, f'заменён; видно счетов: {len(seen)}; {note}')
    return {'result': f'Токен сохранён. Видно счетов: {len(seen)}; {note}.', **status()}


@app.post('/api/run')
def run_now():
    """A pass right now — only in the sandbox (the real account runs on the schedule only)."""
    if C.config()['mode'] != 'sandbox':
        raise HTTPException(409, 'Проход вручную — только в режиме песочницы')
    return {'result': _pass(), **status()}


@app.get('/')
def index():
    return FileResponse(WEB / 'index.html', media_type='text/html')


@app.get('/app.js')
def js():
    return FileResponse(WEB / 'app.js', media_type='text/javascript')


def _pass():
    try:
        r = E.run()
    except Exception as x:
        r = f'ошибка: {type(x).__name__}: {str(x)[:160]}'
    with C.Lock('config.lock'):
        c = C.load('control.json', {}) or {}
        c['last_pass'] = {'at': _now().isoformat(timespec='seconds'), 'result': r}
        C.save('control.json', c)
    return r


def in_window(now=None):
    n = (now or _now()).astimezone(MSK)
    return n.weekday() < 5 and (10, 5) <= (n.hour, n.minute) <= (18, 30)


def _scheduler():
    while True:
        if in_window() and C.config()['mode'] in ('sandbox', 'live'):
            _pass()
        time.sleep(300)


@app.on_event('startup')
def _start():
    if os.environ.get('CLIENT_SCHEDULER', '1') != '0':
        threading.Thread(target=_scheduler, name='executor', daemon=True).start()
