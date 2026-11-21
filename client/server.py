"""The strategy server: the only thing the client gets from it is the published decision (contract client-decision-v1):
final target weights and dates. The client never sends its token, account or positions to the server."""
import datetime as dt, json, urllib.error, urllib.request
from . import config as C

CONTRACT = 'client-decision-v1'


class Bad(ValueError):
    pass


def validate(d, today=None):
    """Checks of a decision before it may be executed; returns it or raises Bad."""
    if d.get('contract') != CONTRACT:
        raise Bad(f"unknown contract {d.get('contract')!r}")
    w = d.get('weights') or {}
    if not w or any((not isinstance(x, (int, float))) or x < 0 for x in w.values()):
        raise Bad('weights are empty or negative')
    if abs(sum(w.values()) - 1) > 0.01:
        raise Bad(f'weights sum to {sum(w.values()):.4f}, not 1')
    if not d.get('cash_ticker'):
        raise Bad('no cash ticker')
    dt.date.fromisoformat(d['decision_date'])
    dt.date.fromisoformat(d['valid_until'])
    return d


def expired(d, today=None):
    today = today or dt.date.today().isoformat()
    return today > d['valid_until']


def fetch(timeout=10):
    """The current decision from the server (cached for the interface and for top-ups while the server is unreachable)."""
    cfg = C.config()
    try:
        key = C.SERVER_KEY.read_text().strip()
    except OSError:
        raise Bad('server key is missing') from None
    req = urllib.request.Request(cfg['server_url'].rstrip('/') + '/api/v1/decision', headers={'Authorization': 'Bearer ' + key})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            d = json.loads(r.read())
    except urllib.error.HTTPError as e:
        raise Bad(f'server HTTP {e.code}') from None
    except (OSError, ValueError) as e:
        raise Bad(f'server unreachable ({type(e).__name__})') from None
    validate(d)
    C.save('decision_cache.json', {**d, 'fetched_at': dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds')})
    return d


def cached():
    return C.load('decision_cache.json')
