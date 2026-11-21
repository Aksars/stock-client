// T-Invest (T-Bank broker) REST API. This is the ONLY place that reads the user's token, and the token is sent
// ONLY to https://invest-public-api.tbank.ru (the constant tinvestBase below), in the Authorization header.
// The token is never logged, never returned to the interface and never sent anywhere else.
//
// Methods used (all under tinvestBase): UsersService/GetAccounts, OperationsService/GetPortfolio,
// OperationsService/GetOperations (sandbox mirroring of deposits), InstrumentsService/FindInstrument,
// InstrumentsService/GetInstrumentBy, MarketDataService/GetLastPrices, MarketDataService/GetTradingStatus,
// OrdersService/GetOrders, OrdersService/PostOrder (live only), SandboxService/* (sandbox only).
// No method that moves money out of the account exists in this program.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const tinvestBase = "https://invest-public-api.tbank.ru/rest/tinkoff.public.invest.api.contract.v1."

// Public root certificates of the T-Bank API chain that are missing from some systems (Russian Trusted Root CA
// and GlobalSign GCC R3 DV TLS CA 2020). They are added to the system pool, not instead of it.
//
//go:embed tbank-extra-ca.pem
var extraCA []byte

var (
	httpBroker *http.Client
	instMu     sync.Mutex
	instCache  = map[string]Instrument{}
)

func init() {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(extraCA)
	httpBroker = &http.Client{Timeout: 15 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type BrokerError struct{ Msg string }

func (e *BrokerError) Error() string { return e.Msg }

func hasToken() bool { return readSecret("tinvest-trade.txt") != "" }

// tcall posts one API method. token "" means the saved token.
func tcall(method string, body any, token string) (map[string]any, error) {
	if token == "" {
		token = readSecret("tinvest-trade.txt")
	}
	if token == "" {
		return nil, &BrokerError{"no T-Invest token"}
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", tinvestBase+method, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpBroker.Do(req)
	if err != nil {
		return nil, &BrokerError{"T-Invest unreachable"}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // a short ticker search ("T") returns several MB
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != 200 {
		msg, _ := out["message"].(string)
		if len(msg) > 120 {
			msg = msg[:120]
		}
		return nil, &BrokerError{fmt.Sprintf("T-Invest HTTP %d %s", resp.StatusCode, msg)}
	}
	if out == nil {
		return nil, &BrokerError{fmt.Sprintf("T-Invest: unreadable answer (%d bytes)", len(raw))}
	}
	return out, nil
}

// q converts a Quotation/MoneyValue {"units":"12","nano":340000000} to 12.34.
func q(v any) float64 {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	return num(m["units"]) + num(m["nano"])/1e9
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func str(v any) string { s, _ := v.(string); return s }

func list(v any) []map[string]any {
	a, _ := v.([]any)
	out := make([]map[string]any, 0, len(a))
	for _, x := range a {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func money(x float64) map[string]any {
	u := math.Floor(x)
	return map[string]any{"currency": "rub", "units": strconv.FormatInt(int64(u), 10), "nano": int64(math.Round((x - u) * 1e9))}
}

func quotation(x float64) map[string]any {
	u := math.Floor(x)
	return map[string]any{"units": strconv.FormatInt(int64(u), 10), "nano": int64(math.Round((x - u) * 1e9))}
}

type Account struct{ ID, Name, Type, Status, Access string }

func (a Account) tradable() bool {
	return a.Status == "ACCOUNT_STATUS_OPEN" && a.Access == "ACCOUNT_ACCESS_LEVEL_FULL_ACCESS"
}

// getAccounts is a variable so that tests can replace it.
var getAccounts = func(token string) ([]Account, error) {
	r, err := tcall("UsersService/GetAccounts", map[string]any{}, token)
	if err != nil {
		return nil, err
	}
	var out []Account
	for _, a := range list(r["accounts"]) {
		out = append(out, Account{str(a["id"]), str(a["name"]), str(a["type"]), str(a["status"]), str(a["accessLevel"])})
	}
	return out, nil
}

type Instrument struct {
	UID   string
	Board string
	Lot   int
	Step  float64
}

var boards = []string{"TQBR", "TQTF", "TQTD", "TQIF", "TQPI"} // Moscow Exchange boards of shares and funds

func instrument(ticker string) (Instrument, error) {
	instMu.Lock()
	if i, ok := instCache[ticker]; ok {
		instMu.Unlock()
		return i, nil
	}
	instMu.Unlock()
	r, err := tcall("InstrumentsService/FindInstrument", map[string]any{"query": ticker, "apiTradeAvailableFlag": true}, "")
	if err != nil {
		return Instrument{}, err
	}
	best, rank := map[string]any(nil), 99
	for _, x := range list(r["instruments"]) {
		if str(x["ticker"]) != ticker {
			continue
		}
		for k, b := range boards {
			if str(x["classCode"]) == b && k < rank {
				best, rank = x, k
			}
		}
	}
	if best == nil {
		return Instrument{}, &BrokerError{ticker + ": instrument not found"}
	}
	r2, err := tcall("InstrumentsService/GetInstrumentBy", map[string]any{"idType": "INSTRUMENT_ID_TYPE_UID", "id": str(best["uid"])}, "")
	if err != nil {
		return Instrument{}, err
	}
	it, _ := r2["instrument"].(map[string]any)
	in := Instrument{UID: str(best["uid"]), Board: boards[rank], Lot: int(num(it["lot"])), Step: q(it["minPriceIncrement"])}
	if in.Lot <= 0 {
		in.Lot = 1
	}
	if in.Step <= 0 {
		in.Step = 0.01
	}
	instMu.Lock()
	instCache[ticker] = in
	instMu.Unlock()
	return in, nil
}

func lastPrice(uid string) (float64, error) {
	r, err := tcall("MarketDataService/GetLastPrices", map[string]any{"instrumentId": []string{uid}}, "")
	if err != nil {
		return 0, err
	}
	l := list(r["lastPrices"])
	if len(l) == 0 {
		return 0, nil
	}
	return q(l[0]["price"]), nil
}

type Position struct {
	Qty   float64 `json:"qty"`
	Price float64 `json:"price"`
}

// portfolio returns positions (ticker -> qty), roubles and the total value of an account.
func portfolio(account string, sandbox bool) (map[string]Position, float64, float64, error) {
	m := "OperationsService/GetPortfolio"
	if sandbox {
		m = "SandboxService/GetSandboxPortfolio"
	}
	r, err := tcall(m, map[string]any{"accountId": account, "currency": "RUB"}, "")
	if err != nil {
		return nil, 0, 0, err
	}
	pos, cash := map[string]Position{}, 0.0
	for _, x := range list(r["positions"]) {
		qty := q(x["quantity"])
		t := str(x["ticker"])
		for len(t) > 0 && t[len(t)-1] == '@' {
			t = t[:len(t)-1]
		}
		if str(x["instrumentType"]) == "currency" {
			if t == "RUB000UTSTOM" || str(x["figi"]) == "RUB000UTSTOM" {
				cash += qty
			}
			continue
		}
		if qty != 0 {
			pos[t] = Position{qty, q(x["currentPrice"])}
		}
	}
	return pos, cash, q(r["totalAmountPortfolio"]), nil
}

var errNoToken = errors.New("no token")
