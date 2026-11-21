"""Minimal T-Invest REST client with the user's trading token (read at call time, never printed or logged)."""
import json, os, ssl, threading, urllib.error, urllib.request
from . import config as C

BASE = 'https://invest-public-api.tbank.ru/rest/tinkoff.public.invest.api.contract.v1.'
CLASSES = ('TQBR', 'TQTF', 'TQTD', 'TQIF', 'TQPI')   # shares and funds
_ctx = None
_lock = threading.Lock()
_instruments, _steps = {}, {}


class Unavailable(RuntimeError):
    pass


def _context():
    global _ctx
    if _ctx is None:
        ctx = ssl.create_default_context()
        ctx.load_verify_locations(str(C.CA_EXTRA))
        _ctx = ctx
    return _ctx


def has_token():
    return C.TRADE_TOKEN.is_file() and bool(C.TRADE_TOKEN.read_text().strip())


def call(method, body, timeout=10, token=None):
    """token: a candidate token to check before it is saved; default the saved one."""
    try:
        token = token or C.TRADE_TOKEN.read_text().strip()
    except OSError:
        raise Unavailable('trading token is missing') from None
    req = urllib.request.Request(BASE + method, data=json.dumps(body).encode(),
                                 headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', 'accept': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=timeout, context=_context()) as r:
            return json.loads(r.read())
    except urllib.error.HTTPError as e:   # the broker's message, never the request headers
        try:
            msg = json.loads(e.read()).get('message', '')[:120]
        except Exception:
            msg = ''
        raise Unavailable(f'T-Invest HTTP {e.code} {msg}'.strip()) from None
    except (OSError, ValueError) as e:
        raise Unavailable(f'T-Invest {type(e).__name__}') from None


def q(v):
    """Quotation/MoneyValue -> float."""
    if not isinstance(v, dict):
        return None
    try:
        return float(v.get('units', 0)) + float(v.get('nano', 0)) / 1e9
    except (TypeError, ValueError):
        return None


def money(x):
    u = int(x // 1)
    return {'currency': 'rub', 'units': str(u), 'nano': int(round((x - u) * 1e9))}


def price(x):
    u = int(x // 1)
    return {'units': str(u), 'nano': int(round((x - u) * 1e9))}


def instrument(ticker):
    """uid, board, lot and minimum price step of a share or a fund by its exchange ticker."""
    with _lock:
        if ticker in _instruments:
            return _instruments[ticker]
    rows = [x for x in call('InstrumentsService/FindInstrument', {'query': ticker}).get('instruments', [])
            if x.get('ticker') == ticker and x.get('classCode') in CLASSES]
    if not rows:
        raise Unavailable(f'{ticker}: instrument not found')
    rows.sort(key=lambda x: CLASSES.index(x['classCode']))
    item = call('InstrumentsService/GetInstrumentBy', {'idType': 'INSTRUMENT_ID_TYPE_UID', 'id': rows[0]['uid']}).get('instrument', {})
    res = {'uid': rows[0]['uid'], 'board': rows[0]['classCode'], 'lot': int(item.get('lot') or 1), 'step': q(item.get('minPriceIncrement')) or 0.01}
    with _lock:
        _instruments[ticker] = res
    return res


def last_price(uid):
    r = call('MarketDataService/GetLastPrices', {'instrumentId': [uid]}).get('lastPrices') or []
    return q(r[0].get('price')) if r else None


def accounts(token=None):
    return [{'id': a.get('id'), 'name': a.get('name'), 'type': a.get('type'), 'status': a.get('status'), 'access': a.get('accessLevel')}
            for a in call('UsersService/GetAccounts', {}, token=token).get('accounts', [])]


def save_token(token):
    """Write the token to secrets/tinvest-trade.txt (owner-only, atomic). The caller checks it with the broker first."""
    C.SECRETS.mkdir(parents=True, exist_ok=True)
    os.chmod(C.SECRETS, 0o700)
    tmp = C.SECRETS / 'tinvest-trade.pending'
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(token + '\n')
    os.replace(tmp, C.TRADE_TOKEN)


def portfolio(account_id, sandbox):
    """Positions {ticker: {qty, price, uid}}, roubles and total value of an account (sandbox or real)."""
    m = 'SandboxService/GetSandboxPortfolio' if sandbox else 'OperationsService/GetPortfolio'
    p = call(m, {'accountId': account_id, 'currency': 'RUB'})
    pos, cash = {}, 0.0
    for x in p.get('positions', []):
        qty = q(x.get('quantity')) or 0.0
        t = (x.get('ticker') or '').rstrip('@')
        if x.get('instrumentType') == 'currency':
            if t == 'RUB000UTSTOM' or x.get('figi') == 'RUB000UTSTOM':
                cash += qty
            continue
        if qty:
            pos[t] = {'qty': qty, 'price': q(x.get('currentPrice')), 'uid': x.get('instrumentUid')}
    return pos, cash, q(p.get('totalAmountPortfolio')) or 0.0
