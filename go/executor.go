// Execution of the server's decision on the user's account (executor-v1).
//
// Modes: "off" — nothing; "sandbox" — the broker's virtual account of the user's token (paid in the value of the real
// account, later deposits mirrored); "live" — real limit orders to ONE account (config account_id), only if the token
// has full access to it. Only the user switches to "live" (fresh account check + typed phrase, see api.go).
//
// Who decides that orders go out:
//   - auto_execute = false (default): a new plan is only SHOWN ("pending"); orders go out when the user clicks
//     "Yes, execute" (POST /api/confirm), with fresh prices;
//   - auto_execute = true: the scheduler executes a new decision itself during trading hours.
//
// Guards: a decision is executed once (decision_id); never after its valid_until; new money (cash > 3%) is invested by
// the last executed decision for up to 8 days after its date; the stop flag halts everything, also between orders;
// only "managed" stocks are traded — the target, what the client itself bought, and what the user handed over in the
// interface; other holdings are never touched; live: no new orders while the account has active ones, one order at
// most max_order_rub; nothing is posted while the market is closed.
package main

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ExecutorPolicy = "executor-v1"
	TopUp          = 0.03
	TopUpAge       = 8
)

// Plan is everything up to the orders. Result != "" means there is nothing to do (and why).
type Plan struct {
	Result    string              `json:"result,omitempty"`
	BuiltAt   string              `json:"built_at"`
	Mode      string              `json:"mode"`
	Account   string              `json:"account,omitempty"`
	Decision  *Decision           `json:"decision,omitempty"`
	TopUp     bool                `json:"topup,omitempty"`
	Orders    []Order             `json:"orders"`
	Skipped   []string            `json:"skipped"`
	Positions map[string]Position `json:"positions,omitempty"`
	Cash      float64             `json:"cash"`
	Total     float64             `json:"total"`
	Seen      []Account           `json:"accounts_seen,omitempty"`
}

func today() string { return time.Now().In(msk).Format("2006-01-02") }

var msk = time.FixedZone("MSK", 3*3600)

func liveAccount(cfg Config) (*Account, []Account, error) {
	seen, err := getAccounts("")
	if err != nil {
		return nil, nil, err
	}
	for _, a := range seen {
		if cfg.AccountID != "" && a.ID == cfg.AccountID && a.tradable() {
			a := a
			return &a, seen, nil
		}
	}
	return nil, seen, nil
}

// setupSandbox opens the broker's virtual account once and pays in the value of the configured real account.
func setupSandbox(cfg Config) (State, error) {
	st := loadState(false)
	if st.Account != "" {
		return st, nil
	}
	a, _, err := liveAccount(cfg)
	if err != nil {
		return st, err
	}
	if a == nil {
		return st, fmt.Errorf("нет настроенного счёта с полным доступом: не знаю, сколько внести в песочницу")
	}
	_, _, total, err := portfolio(a.ID, false)
	if err != nil {
		return st, err
	}
	if total <= 0 {
		total = 1_000_000
	}
	r, err := tcall("SandboxService/OpenSandboxAccount", map[string]any{"name": "stock-client — проверка"}, "")
	if err != nil {
		return st, err
	}
	acc := str(r["accountId"])
	if _, err := tcall("SandboxService/SandboxPayIn", map[string]any{"accountId": acc, "amount": money(total)}, ""); err != nil {
		return st, err
	}
	st = State{Account: acc, Opened: today(), PaidIn: round2(total)}
	return st, saveJSON(stateName(false), st)
}

// mirrorInputs pays into the sandbox the deposits made to the real account since the sandbox was opened.
func mirrorInputs(cfg Config, st *State) {
	r, err := tcall("OperationsService/GetOperations", map[string]any{"accountId": cfg.AccountID, "from": st.Opened + "T00:00:00Z",
		"to": time.Now().UTC().Format("2006-01-02T15:04:05Z"), "state": "OPERATION_STATE_EXECUTED"}, "")
	if err != nil {
		return
	}
	in := 0.0
	for _, o := range list(r["operations"]) {
		if str(o["operationType"]) == "OPERATION_TYPE_INPUT" {
			in += q(o["payment"])
		}
	}
	if add := round2(in - st.MirroredIn); add >= 1 {
		if _, err := tcall("SandboxService/SandboxPayIn", map[string]any{"accountId": st.Account, "amount": money(add)}, ""); err == nil {
			st.MirroredIn = round2(in)
			saveJSON(stateName(false), st)
		}
	}
}

// pickDecision: a new decision from the server, or the last executed one when new money came in.
func pickDecision(st State, cash, total float64, day string) (*Decision, bool, string) {
	topup := st.LastDecision != "" && total > 0 && cash/total > TopUp
	d, err := fetchDecision()
	if err != nil {
		d = cachedDecision()
		if d == nil || !topup || d.DecisionID != st.LastDecision {
			return nil, topup, "сервер: " + err.Error() + " — ничего не отправлено"
		}
	}
	if d.DecisionID == st.LastDecision {
		if !topup {
			return nil, false, "решение " + d.DecisionDate + " уже исполнено"
		}
		a, _ := time.Parse("2006-01-02", d.DecisionDate)
		b, _ := time.Parse("2006-01-02", day)
		if b.Sub(a).Hours()/24 > TopUpAge {
			return nil, true, fmt.Sprintf("решение %s старше %d дней — новые деньги ждут следующего", d.DecisionDate, TopUpAge)
		}
		return d, true, ""
	}
	if expired(d, day) {
		return nil, topup, "решение " + d.DecisionDate + " действовало до " + d.ValidUntil + " — ничего не отправлено"
	}
	return d, topup, ""
}

// managed: the stocks the client may trade on this account.
func managed(d *Decision, st State, cfg Config) map[string]bool {
	m := map[string]bool{d.CashTicker: true}
	for t := range d.Weights {
		m[t] = true
	}
	for _, t := range st.Traded {
		m[t] = true
	}
	for _, t := range cfg.ManagedExtra {
		m[t] = true
	}
	return m
}

// buildPlan reads the account and the decision and computes the orders. It posts nothing.
// check=true: read the REAL account (for the account check before enabling live) and ignore what was executed.
func buildPlan(cfg Config, live, check bool) Plan {
	p := Plan{BuiltAt: time.Now().UTC().Format(time.RFC3339), Mode: cfg.Mode, Orders: []Order{}, Skipped: []string{}}
	var st State
	if live {
		a, seen, err := liveAccount(cfg)
		if err != nil {
			p.Result = err.Error()
			return p
		}
		p.Seen = seen
		if a == nil {
			p.Result = fmt.Sprintf("реальный счёт: токен видит %d счёт(а), настроенного открытого с полным доступом среди них нет", len(seen))
			return p
		}
		p.Account, st = a.ID, loadState(true)
		if check {
			st.LastDecision = ""
		} else if r, err := tcall("OrdersService/GetOrders", map[string]any{"accountId": a.ID}, ""); err != nil || len(list(r["orders"])) > 0 {
			p.Result = "на счёте есть активные заявки (или их не удалось прочитать) — новых не отправляю"
			return p
		}
	} else {
		var err error
		if st, err = setupSandbox(cfg); err != nil {
			p.Result = err.Error()
			return p
		}
		mirrorInputs(cfg, &st)
		p.Account = st.Account
	}
	pos, cash, total, err := portfolio(p.Account, !live)
	if err != nil {
		p.Result = err.Error()
		return p
	}
	p.Positions, p.Cash, p.Total = pos, round2(cash), total
	d, topup, note := pickDecision(st, cash, total, today())
	if d == nil {
		p.Result = note
		return p
	}
	p.Decision, p.TopUp = d, topup
	ours := managed(d, st, cfg)
	info := map[string]Info{}
	var names []string
	for t := range ours {
		if _, held := pos[t]; held || d.Weights[t] > 0 {
			names = append(names, t)
		}
	}
	sort.Strings(names)
	for _, t := range names {
		in, err := instrument(t)
		if err != nil {
			p.Skipped = append(p.Skipped, t+": "+err.Error())
			continue
		}
		px, err := lastPrice(in.UID)
		if err != nil || px <= 0 {
			p.Skipped = append(p.Skipped, t+": нет цены")
			continue
		}
		info[t] = Info{UID: in.UID, Lot: in.Lot, Px: px, Step: in.Step, Have: pos[t].Qty}
	}
	held := false
	for t := range pos {
		if !ours[t] {
			p.Skipped = append(p.Skipped, t+": не под управлением стратегии — не трогаю")
		} else {
			held = true
		}
	}
	orders, skip := planOrders(d.Weights, info, cash, total, d.CashTicker, held)
	p.Skipped = append(p.Skipped, skip...)
	if live {
		for i := range orders {
			if orders[i].Value > cfg.MaxOrderRub {
				n := int(cfg.MaxOrderRub / (orders[i].Price * float64(orders[i].Lot)))
				p.Skipped = append(p.Skipped, fmt.Sprintf("%s: заявка урезана до %d лот(ов) — предел %.0f ₽", orders[i].Ticker, n, cfg.MaxOrderRub))
				orders[i].Lots, orders[i].Value = n, round2(float64(n*orders[i].Lot)*orders[i].Price)
			}
		}
	}
	for _, o := range orders {
		if o.Lots > 0 {
			p.Orders = append(p.Orders, o)
		}
	}
	return p
}

// execute posts the orders of a plan and records the pass. Called with execMu held.
func execute(cfg Config, live bool, p Plan) string {
	if len(p.Orders) > 0 {
		r, err := tcall("MarketDataService/GetTradingStatus", map[string]any{"instrumentId": p.Orders[0].UID}, "")
		if err != nil || r["limitOrderAvailableFlag"] != true {
			return fmt.Sprintf("биржа закрыта для %s: %d заявок не отправлены", p.Orders[0].Ticker, len(p.Orders))
		}
	}
	var sent []Order
	for _, o := range p.Orders {
		time.Sleep(1500 * time.Millisecond) // the broker limits the request rate
		if c := loadConfig(); c.Stop || c.Mode != cfg.Mode {
			p.Skipped = append(p.Skipped, "остановлено из интерфейса — остальные заявки не отправлены")
			break
		}
		o.OrderID = newID()
		dir := "ORDER_DIRECTION_SELL"
		if o.Side == "buy" {
			dir = "ORDER_DIRECTION_BUY"
		}
		method := "SandboxService/PostSandboxOrder"
		if live {
			method = "OrdersService/PostOrder"
		}
		r, err := tcall(method, map[string]any{"accountId": p.Account, "instrumentId": o.UID, "quantity": fmt.Sprint(o.Lots),
			"price": quotation(o.Price), "direction": dir, "orderType": "ORDER_TYPE_LIMIT", "orderId": o.OrderID}, "")
		if err != nil {
			o.Status, o.Error = "error", err.Error()
		} else {
			o.Status, o.FilledLots = str(r["executionReportStatus"]), fmt.Sprint(r["lotsExecuted"])
		}
		sent = append(sent, o)
	}
	var log []map[string]any
	loadJSON("exec_log.json", &log)
	log = append(log, map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "mode": cfg.Mode, "account": p.Account, "policy": ExecutorPolicy,
		"decision": p.Decision.DecisionDate, "decision_id": p.Decision.DecisionID, "topup": p.TopUp, "target": p.Decision.Weights,
		"before": map[string]any{"positions": p.Positions, "cash": p.Cash, "total": p.Total}, "orders": sent, "skipped": p.Skipped})
	if len(log) > 500 {
		log = log[len(log)-500:]
	}
	saveJSON("exec_log.json", log)
	blocked := false
	for _, s := range p.Skipped {
		blocked = blocked || strings.Contains(s, "ничего не отправлено")
	}
	ok := false
	for _, o := range sent {
		ok = ok || o.Status != "error"
	}
	if ok || (len(p.Orders) == 0 && !blocked) { // a blocked pass is retried next time
		st := loadState(live)
		st.LastDecision, st.Pending = p.Decision.DecisionID, nil
		for t, w := range p.Decision.Weights {
			if w > 0 && !contains(st.Traded, t) {
				st.Traded = append(st.Traded, t)
			}
		}
		sort.Strings(st.Traded)
		saveJSON(stateName(live), st)
	}
	return fmt.Sprintf("%s: %d заявок по решению %s, пропусков %d", cfg.Mode, len(sent), p.Decision.DecisionDate, len(p.Skipped))
}

// pass is one scheduled step: build the plan; execute it if auto-execution is on, otherwise keep it pending.
func pass() string {
	if !execMu.TryLock() {
		return "уже идёт другой проход"
	}
	defer execMu.Unlock()
	cfg := loadConfig()
	if (cfg.Mode != "sandbox" && cfg.Mode != "live") || cfg.Stop {
		return "режим " + cfg.Mode + stopNote(cfg) + ": ничего не отправлено"
	}
	if !hasToken() {
		return "нет торгового токена: ничего не отправлено"
	}
	live := cfg.Mode == "live"
	p := buildPlan(cfg, live, false)
	if p.Result != "" {
		return p.Result
	}
	if !cfg.AutoExecute && len(p.Orders) > 0 {
		st := loadState(live)
		st.Pending = &p
		saveJSON(stateName(live), st)
		return fmt.Sprintf("план по решению %s ждёт подтверждения: %d заявок", p.Decision.DecisionDate, len(p.Orders))
	}
	return execute(cfg, live, p)
}

// confirm is the user's "Yes, execute": the plan is rebuilt with fresh prices and posted if it is still the same decision.
func confirm(decisionID string) (string, error) {
	if !execMu.TryLock() {
		return "", fmt.Errorf("уже идёт другой проход")
	}
	defer execMu.Unlock()
	cfg := loadConfig()
	if (cfg.Mode != "sandbox" && cfg.Mode != "live") || cfg.Stop {
		return "", fmt.Errorf("исполнение выключено или остановлено")
	}
	live := cfg.Mode == "live"
	p := buildPlan(cfg, live, false)
	if p.Result != "" {
		return "", fmt.Errorf("%s", p.Result)
	}
	if p.Decision.DecisionID != decisionID {
		return "", fmt.Errorf("решение сменилось — посмотрите новый план")
	}
	return execute(cfg, live, p), nil
}

func stopNote(c Config) string {
	if c.Stop {
		return " (стоп)"
	}
	return ""
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
