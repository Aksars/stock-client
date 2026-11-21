import datetime as dt
from client import app as A

MSK = dt.timezone(dt.timedelta(hours=3))

def test_window():
    assert A.in_window(dt.datetime(2026, 11, 23, 10, 5, tzinfo=MSK))
    assert not A.in_window(dt.datetime(2026, 11, 23, 10, 4, tzinfo=MSK))
    assert not A.in_window(dt.datetime(2026, 11, 23, 18, 31, tzinfo=MSK))
    assert not A.in_window(dt.datetime(2026, 11, 21, 12, 0, tzinfo=MSK))   # Saturday

def test_only_known_routes():
    paths = {getattr(r, 'path', '') for r in A.app.routes}
    assert {'/api/status', '/api/check', '/api/mode', '/api/stop', '/api/limit', '/api/run', '/api/decision/refresh', '/api/token', '/', '/app.js'} == paths

def test_live_needs_phrase_and_fresh_check(tmp_path, monkeypatch):
    import pytest
    from fastapi import HTTPException
    from client import config as C
    monkeypatch.setattr(C, 'DATA', tmp_path)
    C.save('config.json', {'mode': 'sandbox', 'account_id': '1'})
    with pytest.raises(HTTPException) as x:
        A.set_mode(A.Mode(mode='live', confirm='нет'))
    assert x.value.status_code == 409
    with pytest.raises(HTTPException) as x:
        A.set_mode(A.Mode(mode='live', confirm='реальный счёт'))
    assert 'проверьте' in x.value.detail
    assert C.config()['mode'] == 'sandbox'
    A.set_stop(A.Stop(stop=True))
    assert C.config()['stop'] is True


def test_token_is_checked_saved_privately_and_never_returned(tmp_path, monkeypatch):
    import os, pytest
    from fastapi import HTTPException
    from client import config as C, tinvest as T
    monkeypatch.setattr(C, 'DATA', tmp_path / 'data'); monkeypatch.setattr(C, 'SECRETS', tmp_path / 's'); monkeypatch.setattr(C, 'TRADE_TOKEN', tmp_path / 's' / 't.txt')
    C.save('config.json', {'mode': 'off', 'account_id': '42'})
    good = 't.' + 'a' * 40
    def acc(token=None):
        if token != good:
            raise T.Unavailable('T-Invest HTTP 401')
        return [{'id': '42', 'access': 'ACCOUNT_ACCESS_LEVEL_FULL_ACCESS', 'status': 'ACCOUNT_STATUS_OPEN', 'name': 'x', 'type': 'y'}]
    monkeypatch.setattr(T, 'accounts', acc)
    for bad, code in (('short', 422), ('t.' + 'b' * 40, 400)):
        with pytest.raises(HTTPException) as x:
            A.set_token(A.Token(token=bad))
        assert x.value.status_code == code
    assert not C.TRADE_TOKEN.exists()
    r = A.set_token(A.Token(token='  ' + good + '\n'))
    assert good not in str(r) and 'полный доступ к счёту 42' in r['result']
    assert C.TRADE_TOKEN.read_text().strip() == good and oct(os.stat(C.TRADE_TOKEN).st_mode & 0o777) == '0o600'
    C.save('config.json', {'mode': 'sandbox', 'account_id': '42'})
    with pytest.raises(HTTPException) as x:
        A.set_token(A.Token(token=good))
    assert x.value.status_code == 409
