"""Report of the comparison between the old executor and the new client (exec-compare-v1). Reads the latest old-*.json and
new-*.json snapshots and the decision the client has (data/decision_cache.json); writes report-<date>.md next to them.
What is compared: (1) the weights each sandbox account holds against the target, (2) the orders of the passes since --since,
(3) errors and skipped notes. The accounts started from different holdings (the old one has executed earlier decisions), so the
orders may differ legitimately; the weights after execution are the main check. Run: python3 tools/compare_exec.py [--since YYYY-MM-DD]"""
import datetime as dt,glob,json,sys
from pathlib import Path
DIR=Path('/workspace/output/stock-client/exec-compare')
def latest(side):
    f=sorted(glob.glob(str(DIR/f'{side}-*.json')));return json.loads(Path(f[-1]).read_text()) if f else None
def pct(x):return f'{100*x:.1f}%'
def main(argv):
    since=argv[argv.index('--since')+1] if '--since' in argv else '2000-01-01'
    old,new=latest('old'),latest('new');dec=json.loads(Path('/workspace/data/decision_cache.json').read_text())
    tgt=dec['weights'];L=[f"# Сверка исполнителей — {dt.date.today().isoformat()}",'',
       f"Решение сервера: {dec['decision_date']} (id {dec['decision_id']}), действует до {dec['valid_until']}.",
       f"Снимки: старый {old['at'] if old else '—'}, новый {new['at'] if new else '—'}.",'','## Доли после исполнения','',
       '| Бумага | Цель | Старый | Новый | Старый − цель | Новый − цель |','|---|---|---|---|---|---|']
    tick=sorted(set(tgt)|set((old or {}).get('weights',{}))|set((new or {}).get('weights',{})))
    dev={'old':0.0,'new':0.0}
    for t in tick:
        w0=(old or {}).get('weights',{}).get(t,0.0);w1=(new or {}).get('weights',{}).get(t,0.0);g=tgt.get(t,0.0)
        dev['old']+=abs(w0-g);dev['new']+=abs(w1-g)
        L.append(f'| {t} | {pct(g)} | {pct(w0)} | {pct(w1)} | {100*(w0-g):+.1f} п.п. | {100*(w1-g):+.1f} п.п. |')
    cash=lambda snap:pct(snap['cash']/snap['total']) if snap and snap.get('total') else '—'
    L.append(f'| деньги | 0% | {cash(old)} | {cash(new)} | | |')
    L+=['',f"Сумма отклонений от цели (половина — доля портфеля «не на месте»): старый {100*dev['old']/2:.1f}%, новый {100*dev['new']/2:.1f}%.",
        f"Портфель: старый {old['total'] if old else '—'} ₽, новый {new['total'] if new else '—'} ₽.",'']
    for name,snap in (('Старый исполнитель (broker_exec)',old),('Новый клиент (Go)',new)):
        L+=[f'## {name}: проходы с {since}','']
        runs=[r for r in (snap or {}).get('runs',[]) if r['at'][:10]>=since]
        if not runs:L+=['Проходов с заявками нет.','']
        for r in runs:
            L.append(f"- {r['at']} — решение {r['decision']}: "+(', '.join(f"{o['side']} {o['ticker']} {o['lots']}×{o.get('lot') or 1} по {o['price']} ({o.get('status') or '—'}{', '+o['error'] if o.get('error') else ''})" for o in r['orders']) or 'без заявок'))
            for s in r.get('skipped') or []:L.append(f'  - пропуск: {s}')
        L.append('')
    errs=[(n,o) for n,snap in (('старый',old),('новый',new)) for r in (snap or {}).get('runs',[]) if r['at'][:10]>=since for o in r['orders'] if o.get('status')=='error']
    L+=['## Ошибки заявок','']+([f'- {n}: {o["ticker"]} {o["side"]} — {o.get("error")}' for n,o in errs] or ['Нет.'])
    out=DIR/f'report-{dt.date.today().isoformat()}.md';out.write_text('\n'.join(L)+'\n');print(out)
if __name__=='__main__':main(sys.argv[1:])
