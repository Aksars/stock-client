"""Snapshot of the NEW client (Go client, sandbox mode) for the comparison with the old executor (exec-compare-v1, 21.11.2026).
Read-only: the client's sandbox portfolio through the user's token (SandboxService/GetSandboxPortfolio), data/state_sandbox.json,
data/exec_log.json. Writes /workspace/output/stock-client/exec-compare/new-<UTC time>.json in the same format as the server's
app/scripts/exec_snapshot.py. Development tool, not part of the client program. Run: python3 tools/exec_snapshot.py [--since YYYY-MM-DD]"""
import datetime as dt,json,ssl,sys,urllib.request
from pathlib import Path
HOME=Path('/workspace');OUT=HOME/'output/stock-client/exec-compare'
BASE='https://invest-public-api.tbank.ru/rest/tinkoff.public.invest.api.contract.v1.'
def call(method,body):
    ctx=ssl.create_default_context();ctx.load_verify_locations(str(HOME/'go/tbank-extra-ca.pem'))
    tok=(HOME/'secrets/tinvest-trade.txt').read_text().strip()
    r=urllib.request.Request(BASE+method,data=json.dumps(body).encode(),headers={'Authorization':'Bearer '+tok,'Content-Type':'application/json'})
    with urllib.request.urlopen(r,timeout=15,context=ctx) as f:return json.loads(f.read())
def q(v):return float(v.get('units',0))+float(v.get('nano',0))/1e9 if isinstance(v,dict) else 0.0
def main(argv):
    since=argv[argv.index('--since')+1] if '--since' in argv else '2000-01-01'
    st=json.loads((HOME/'data/state_sandbox.json').read_text());acc=st['account']
    p=call('SandboxService/GetSandboxPortfolio',{'accountId':acc,'currency':'RUB'});pos={};cash=0.0
    for x in p.get('positions',[]):
        t=(x.get('ticker') or '').rstrip('@');qty=q(x.get('quantity'))
        if x.get('instrumentType')=='currency':
            if t=='RUB000UTSTOM' or x.get('figi')=='RUB000UTSTOM':cash+=qty
            continue
        if qty:px=q(x.get('currentPrice'));pos[t]={'qty':qty,'price':px,'value':round(qty*px,2)}
    total=q(p.get('totalAmountPortfolio'))
    lp=HOME/'data/exec_log.json';log=json.loads(lp.read_text()) if lp.exists() else []
    runs=[{'at':e['at'],'decision':e.get('decision'),'target':e.get('target'),'orders':[{k:o.get(k) for k in ('ticker','side','lots','lot','price','value','status','error')} for o in e.get('orders') or []],'skipped':e.get('skipped') or []}
          for e in log if e.get('mode')=='sandbox' and e['at'][:10]>=since]
    snap={'side':'new','executor':'stock-client (Go)','at':dt.datetime.now(dt.timezone.utc).isoformat(timespec='seconds'),'account':acc,
          'positions':pos,'cash':round(cash,2),'total':round(total,2),'weights':{t:round(v['value']/total,4) for t,v in pos.items()} if total else {},'runs':runs,
          'last_decision':st.get('last_decision')}
    OUT.mkdir(parents=True,exist_ok=True);f=OUT/f"new-{snap['at'].replace(':','')}.json";f.write_text(json.dumps(snap,ensure_ascii=False,indent=1));print(f)
if __name__=='__main__':main(sys.argv[1:])
