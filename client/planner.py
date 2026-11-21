"""Rounding of the target weights to whole lots (planner-v1; the rule of broker-exec-v2..v5 without the strategy's risk data, user's
decision 21.11.2026: the client gets only the weights). Pure function: no network, everything comes in arguments.

1. Every stock of the target is rounded down to whole lots.
2. One lot at a time is added to the stock whose weight it brings closest to the target, while
   - the stock part stays within min(1, stock share of the target + 5 p.p.) - 0.5% buffer
     (the stock share of the target = 1 - the cash ticker's weight; the server derived it from risk.eq, which equals it), and
   - one stock stays within 50% of the stock part (of the target stock part while filling up).
3. What is left goes to the cash ticker (TPAY: no broker fee), keeping a 0.5% buffer.
4. A change is traded only if it is at least 1 lot and 0.5% of the portfolio (a full sale of a stock that left the target always goes);
   limit price = last price +-0.3% snapped to the price step; sells first; buys are cut to the cash available after the sells;
   turnover of stocks above 60% of the portfolio blocks the whole run (except the first fill of an account that holds only cash)."""
import math

MAX_TURN, MIN_W, SLIP, EQ_ALLOW, ONE_MAX, BUFFER = 0.60, 0.005, 0.003, 0.05, 0.50, 0.005


def snap(p, step, up):
    """Buy: not above the limit (round down); sell: not below it (round up)."""
    k = math.floor(p / step + 1e-9) if up else math.ceil(p / step - 1e-9)
    return round(k * step, 9)


def plan(target, info, cash, total, cash_ticker='TPAY', has_positions=True):
    """target: ticker -> weight (sums to 1); info: ticker -> {uid, lot, px, step, have} for every ticker that may be traded
    (target stocks, held stocks of the strategy, the cash ticker). Returns (orders, skipped notes)."""
    skip = []
    if total <= 0:
        return [], ['портфель пуст']
    eq = sum(w for t, w in target.items() if t != cash_ticker)
    stocks = [t for t in target if t != cash_ticker and target[t] > 0]
    for t in [t for t in stocks if t not in info]:
        skip.append(f'{t}: нет цены или лота — пропущена')
    stocks = [t for t in stocks if t in info]
    unit = {t: info[t]['lot'] * info[t]['px'] / total for t in stocks}
    lots = {t: int(target[t] / unit[t]) for t in stocks}
    smax = min(1.0, eq + EQ_ALLOW) - BUFFER
    tgt_part = sum(target[t] for t in stocks)

    def w(t):
        return lots[t] * unit[t]

    while True:
        tot = sum(w(t) for t in stocks)
        best = None
        for t in stocks:
            d = unit[t]
            if tot + d > smax + 1e-9 or w(t) + d > ONE_MAX * max(tot + d, tgt_part) + 1e-9:
                continue
            g = (target[t] - w(t)) ** 2 - (target[t] - w(t) - d) ** 2   # closer to the target
            if g > 0 and (best is None or g > best[0]):
                best = (g, t)
        if not best:
            break
        lots[best[1]] += 1

    want = {t: lots[t] * info[t]['lot'] for t in stocks}
    if cash_ticker in info:
        c = info[cash_ticker]
        left = total * (1 - BUFFER) - sum(want[t] * info[t]['px'] for t in stocks)
        want[cash_ticker] = max(0, int(left / (c['lot'] * c['px']))) * c['lot']
    orders = []
    for t, x in info.items():
        n = int(round((want.get(t, 0) - x['have']) / x['lot']))
        if n == 0 or (abs(n * x['lot'] * x['px']) < MIN_W * total and want.get(t, 0) > 0):
            continue
        buy = n > 0
        orders.append({'ticker': t, 'uid': x['uid'], 'lots': abs(n), 'lot': x['lot'], 'side': 'buy' if buy else 'sell',
                       'price': snap(x['px'] * (1 + SLIP) if buy else x['px'] * (1 - SLIP), x['step'], buy),
                       'value': round(abs(n) * x['lot'] * x['px'], 2)})
    orders.sort(key=lambda o: o['side'] != 'sell')
    turn = sum(o['value'] for o in orders if o['ticker'] != cash_ticker) / total
    if turn > MAX_TURN and has_positions:
        return [], skip + [f'оборот {100 * turn:.0f}% больше предела {100 * MAX_TURN:.0f}% — ничего не отправлено']
    left = cash + sum(o['value'] for o in orders if o['side'] == 'sell')
    for o in orders:
        if o['side'] == 'buy':
            if o['value'] * (1 + SLIP) > left:
                o['lots'] = max(0, int(left / (o['price'] * o['lot'] * (1 + SLIP))))
                o['value'] = round(o['lots'] * o['lot'] * o['price'], 2)
            left -= o['value'] * (1 + SLIP)
    return [o for o in orders if o['lots'] > 0], skip
