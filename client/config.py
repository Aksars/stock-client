"""Paths, settings and small JSON stores of the client (stock-client-v1).

Everything the client knows lives under /workspace (overridable by STOCK_CLIENT_HOME for tests):
  secrets/server-api-key.txt   key for the strategy server (only its SHA-256 is on the server)
  secrets/tinvest-trade.txt    the user's T-Invest trading token (never leaves this machine)
  data/config.json             mode off|sandbox|live, stop, account_id, max_order_rub, server_url
  data/state_sandbox.json, data/state_live.json, data/exec_log.json, data/control.json, data/decision_cache.json
"""
import fcntl, json, os
from pathlib import Path

HOME = Path(os.environ.get('STOCK_CLIENT_HOME', '/workspace'))
SECRETS = HOME / 'secrets'
DATA = HOME / 'data'
SERVER_KEY = SECRETS / 'server-api-key.txt'
TRADE_TOKEN = SECRETS / 'tinvest-trade.txt'
CA_EXTRA = Path(__file__).resolve().parent / 'tbank-extra-ca.pem'   # public certificates: Russian Trusted Root CA + GlobalSign GCC R3 (T-Bank API chain)

DEFAULTS = {'mode': 'off', 'stop': False, 'account_id': None, 'max_order_rub': 100_000, 'server_url': ''}


def load(name, default=None):
    try:
        return json.loads((DATA / name).read_text())
    except (OSError, ValueError):
        return default


def save(name, value):
    DATA.mkdir(parents=True, exist_ok=True)
    tmp = DATA / (name + '.pending')
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=1))
    os.replace(tmp, DATA / name)


def config():
    return {**DEFAULTS, **(load('config.json', {}) or {})}


class Lock:
    """An exclusive file lock (blocking unless nowait)."""
    def __init__(self, name, nowait=False):
        self.name, self.nowait = name, nowait

    def __enter__(self):
        DATA.mkdir(parents=True, exist_ok=True)
        self.f = open(DATA / self.name, 'w')
        fcntl.flock(self.f, fcntl.LOCK_EX | (fcntl.LOCK_NB if self.nowait else 0))
        return self

    def __exit__(self, *a):
        self.f.close()
