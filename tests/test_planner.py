from client import planner as P

def info(px, lot=1, have=0, step=0.01):
    return {'uid': 'u', 'lot': lot, 'px': px, 'have': have, 'step': step}

T75 = {'LKOH': 0.3375, 'X5': 0.3375, 'T': 0.075, 'TPAY': 0.25}

def weights(orders, inf, total):
    have = {t: x['have'] for t, x in inf.items()}
    for o in orders:
        have[o['ticker']] += (1 if o['side'] == 'buy' else -1) * o['lots'] * o['lot']
    return {t: have[t] * inf[t]['px'] / total for t in inf}

def test_stock_cap_follows_the_target_not_a_fixed_half():
    """The server's fallback capped stocks at 54.5% (risk.eq missing); with a 75% stock target the client must reach ~75%."""
    inf = {'LKOH': info(7000), 'X5': info(3000), 'T': info(3000), 'TPAY': info(100)}
    o, skip = P.plan(T75, inf, 1_000_000, 1_000_000, has_positions=False)
    w = weights(o, inf, 1_000_000)
    stocks = w['LKOH'] + w['X5'] + w['T']
    assert 0.73 < stocks <= 0.80 - 0.005 + 1e-9 and w['TPAY'] > 0.2 and not skip

def test_one_stock_at_most_half_of_the_stock_part_and_buffer_kept():
    inf = {'LKOH': info(7000), 'X5': info(3000), 'T': info(3000), 'TPAY': info(100)}
    o, _ = P.plan(T75, inf, 1_000_000, 1_000_000, has_positions=False)
    w = weights(o, inf, 1_000_000)
    part = w['LKOH'] + w['X5'] + w['T']
    assert max(w['LKOH'], w['X5'], w['T']) <= 0.5 * max(part, 0.75) + 1e-9
    assert sum(w.values()) <= 1 - P.BUFFER + 1e-9

def test_expensive_lot_is_rounded_without_overshoot():
    inf = {'GMKN': info(120000, lot=1), 'TPAY': info(100)}
    o, _ = P.plan({'GMKN': 0.25, 'TPAY': 0.75}, inf, 300_000, 300_000, has_positions=False)
    w = weights(o, inf, 300_000)
    assert w['GMKN'] <= 0.30 - 0.005 + 1e-9

def test_sells_first_and_buys_cut_to_cash():
    inf = {'LKOH': info(7000, have=20), 'X5': info(3000), 'TPAY': info(100)}
    o, _ = P.plan({'X5': 0.3, 'TPAY': 0.7}, inf, 100_000, 240_000 / 0.24)   # 140k LKOH + 100k cash + rest elsewhere: buys exceed cash
    assert o[0]['side'] == 'sell' and o[0]['ticker'] == 'LKOH'
    spent = sum(x['value'] * (1 + P.SLIP) for x in o if x['side'] == 'buy')
    assert spent <= 100_000 + 140_000 + 1

def test_turnover_guard_blocks_the_run():
    inf = {'LKOH': info(7000, have=100), 'X5': info(3000), 'TPAY': info(100)}
    o, skip = P.plan({'X5': 0.75, 'TPAY': 0.25}, inf, 0, 700_000, has_positions=True)
    assert o == [] and 'ничего не отправлено' in skip[-1]

def test_first_fill_is_not_blocked_by_turnover():
    inf = {'X5': info(3000), 'TPAY': info(100)}
    o, skip = P.plan({'X5': 0.75, 'TPAY': 0.25}, inf, 700_000, 700_000, has_positions=False)
    assert o and not skip

def test_small_change_is_not_traded():
    inf = {'X5': info(3000, have=250), 'TPAY': info(100, have=2480)}
    o, _ = P.plan({'X5': 0.75, 'TPAY': 0.25}, inf, 4000, 1_000_000)
    assert all(x['ticker'] != 'X5' for x in o)

def test_limit_prices_snap_to_step():
    assert P.snap(100.37, 0.5, True) == 100.0 and P.snap(100.37, 0.5, False) == 100.5

def test_missing_price_is_reported():
    o, skip = P.plan({'X5': 0.5, 'TPAY': 0.5}, {'TPAY': info(100)}, 100_000, 100_000, has_positions=False)
    assert any('X5' in s for s in skip)
