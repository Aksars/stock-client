# AUDIT — how to verify this client

This file is written for a person or an AI model that reviews the client before trusting it with a brokerage token.
Everything below can be checked against the source in this folder (about 1 600 lines of Go, one page of JS).

## 1. What the program is

A single executable built only from the Go standard library (`go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' .`
prints only `stockclient`; there is no `go.sum`). It serves a local page on `127.0.0.1:8240`, asks the strategy server
for target weights and, on the user's click (or automatically if the user switched that on), posts limit orders to the
user's T-Invest account with the user's token.

| File | Role |
|---|---|
| `store.go` | paths, JSON files, config defaults (everything off) |
| `server.go` | the ONLY code that talks to the strategy server |
| `tinvest.go` | the ONLY code that reads the token and talks to the broker |
| `planner.go` | pure arithmetic: weights → whole lots → orders |
| `executor.go` | reads the account, builds the plan, posts orders, records them |
| `api.go`, `web/` | the local interface |
| `main.go` | flags, scheduler, opening the browser |

## 2. Every outgoing connection

There are exactly two `http.Client`s and both refuse redirects:

1. **Strategy server** — `server.go`, `fetchDecision`: `GET {config.server_url}/api/v1/decision`, header
   `Authorization: Bearer <secrets/server-api-key.txt>`. No request body. Nothing else is ever sent to the server.
   Development only: with `server_url = "registry"` the address is read from the local file
   `registry/stock-analysis/client-api.json` (written by the server in the shared folder of the developer's sandboxes).
2. **Broker** — `tinvest.go`, `tcall`: `POST https://invest-public-api.tbank.ru/rest/tinkoff.public.invest.api.contract.v1.<Method>`
   (constant `tinvestBase`), header `Authorization: Bearer <secrets/tinvest-trade.txt>`. Methods used are listed at the
   top of `tinvest.go`; none of them moves money out of the account (there is no withdrawal or transfer call).

How to check: `grep -n "http.NewRequest\|http.Get\|http.Post\|net.Dial" *.go` finds only these two places.
`grep -n "tinvest-trade" *.go` shows the token file is read only in `tinvest.go` (`tcall`, `hasToken`) and written only
by `POST /api/token` in `api.go`. The server key is written only by `POST /api/server-key` and read only by `fetchDecision`.
There is no default server address: the user enters it in «Настройки» (`POST /api/settings`, `https://…`). The token is never put into a log line, a JSON answer or a request to the server.

The only local program started is the default browser (`main.go`, `openBrowser`, can be disabled with `-no-browser`).

## 3. What the server can and cannot make the client do

The server's answer is checked by `validateDecision` (`server.go`) before use: known contract, real tickers only, weights
between 0 and 1 summing to 1, the cash part only in an allowed fund (`TPAY`), no stock above 50% of the portfolio, at most
30 stocks. Then `planner.go` and `executor.go` add their own limits: at most stock share + 5 p.p. in stocks, one stock at
most 50% of the stock part, a stock turnover above 60% blocks the plan, only limit orders at ±0.3% from the last price,
a decision is executed once and never after its `valid_until`, only "managed" stocks are traded (those in the decision,
those the client bought, those the user handed over), other holdings are never touched.

A hostile server can therefore at worst ask for a bad but bounded allocation among exchange-listed stocks; it cannot get
the token, the account number or the positions, and it cannot withdraw money.

## 4. Who decides that orders go out

- Default: mode `off`, `auto_execute` false — nothing is ever posted.
- `sandbox`: the broker's virtual account (no real money).
- `live`: only after a fresh read-only account check (≤ 30 min) and the typed phrase «РЕАЛЬНЫЙ СЧЁТ» (`api.go`, `/api/mode`).
- With `auto_execute` false (default) a new plan only waits; orders go out on the user's click «Да, исполнить»
  (`/api/confirm`). Turning `auto_execute` on for the real account needs the phrase again (`/api/auto`).
- «Аварийная остановка» stops everything, also between orders of a running pass.

## 5. The local API cannot be driven by a web site

`api.go`, `wrap`: the Host must be localhost (DNS rebinding), every POST needs `Content-Type: application/json` and the
header `X-Stock-Client: 1` (a foreign page cannot send these without a CORS preflight, which is never approved), and
`Sec-Fetch-Site: cross-site` is refused. The page is served with a strict Content-Security-Policy and `X-Frame-Options: DENY`.

## 6. Updates

The program has no update mechanism at all: it never downloads or replaces itself. A new version is a new file the user
downloads and starts by hand.

## 7. Reproducible build

`./build.sh` builds with `-trimpath -buildvcs=false -ldflags "-s -w -buildid="` and `CGO_ENABLED=0`; the same source and the
same Go version (`dist/GO_VERSION`) give byte-identical files. Compare your `dist/SHA256SUMS` with the published one to
confirm that a downloaded executable was built from this source.

## 8. Files on disk

`secrets/` (token, server key; written owner-only where the file system supports it) and `data/` (settings, state, journal,
`app.log`) next to the executable or in `-home`. Deleting the folder removes everything the client knows.
