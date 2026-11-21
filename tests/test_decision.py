import pytest
from client import executor as E, server as S

D = {'contract': 'client-decision-v1', 'policy': 'p', 'decision_date': '2026-11-20', 'execute_on': 'x', 'valid_until': '2026-11-24',
     'cash_ticker': 'TPAY', 'weights': {'X5': 0.75, 'TPAY': 0.25}, 'decision_id': 'abc', 'published_at': 'y'}

def test_validate():
    assert S.validate(dict(D))
    for bad in ({'contract': 'v0'}, {'weights': {'X5': 0.5}}, {'weights': {}}, {'weights': {'X5': 1.2, 'TPAY': -0.2}}, {'cash_ticker': ''}):
        with pytest.raises(S.Bad):
            S.validate({**D, **bad})

def test_expired():
    assert not S.expired(D, '2026-11-24') and S.expired(D, '2026-11-25')

def _fetch(d):
    def f():
        if isinstance(d, Exception):
            raise d
        return d
    return f

def test_new_decision_is_taken(monkeypatch):
    monkeypatch.setattr(S, 'fetch', _fetch(D))
    d, topup, note = E.pick_decision({}, 0, 100, '2026-11-21')
    assert d is D and note is None

def test_same_decision_once(monkeypatch):
    monkeypatch.setattr(S, 'fetch', _fetch(D))
    d, _, note = E.pick_decision({'last_decision': 'abc'}, 1, 100, '2026-11-21')
    assert d is None and 'уже исполнено' in note

def test_new_money_by_the_last_decision_within_8_days(monkeypatch):
    monkeypatch.setattr(S, 'fetch', _fetch(D))
    assert E.pick_decision({'last_decision': 'abc'}, 10, 100, '2026-11-27')[0] is D
    assert E.pick_decision({'last_decision': 'abc'}, 10, 100, '2026-11-29')[0] is None

def test_expired_decision_is_not_executed(monkeypatch):
    monkeypatch.setattr(S, 'fetch', _fetch(D))
    d, _, note = E.pick_decision({}, 0, 100, '2026-11-25')
    assert d is None and 'ничего не отправлено' in note

def test_server_down_only_topup_by_cached(monkeypatch):
    monkeypatch.setattr(S, 'fetch', _fetch(S.Bad('down')))
    monkeypatch.setattr(S, 'cached', lambda: D)
    assert E.pick_decision({}, 0, 100, '2026-11-21')[0] is None
    assert E.pick_decision({'last_decision': 'abc'}, 10, 100, '2026-11-22')[0] is D
