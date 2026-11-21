// The local interface: a page and a small JSON API, served on this machine only (default 127.0.0.1:8240).
//
// Protection of the local API (so that a web site open in the same browser cannot drive it):
//   - the Host header must be localhost / 127.0.0.1 / [::1] (blocks DNS rebinding), unless -listen opened it wider;
//   - every POST must carry "Content-Type: application/json" and the header "X-Stock-Client: 1" — a foreign page
//     cannot send such a request without a CORS preflight, which this server never approves;
//   - requests marked Sec-Fetch-Site: cross-site are refused.
//
// Nothing here talks to the strategy server or the broker by itself: the handlers call server.go / tinvest.go /
// executor.go. The token is accepted by POST /api/token and is never returned by any endpoint.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

const (
	Version     = "stock-client-go-v1"
	Phrase      = "РЕАЛЬНЫЙ СЧЁТ"
	CheckMaxMin = 30
)

var allowWideHost = false // set by -listen with a non-loopback address (development in a container)

type Control struct {
	History  []map[string]any `json:"history"`
	Check    *Check           `json:"check,omitempty"`
	LastPass map[string]any   `json:"last_pass,omitempty"`
}

type Check struct {
	At      string `json:"at"`
	OK      bool   `json:"ok"`
	Account string `json:"account"`
	Reason  string `json:"reason,omitempty"`
	Plan    *Plan  `json:"plan,omitempty"`
}

func loadControl() Control { var c Control; loadJSON("control.json", &c); return c }

func change(what string, from, to any) {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := loadControl()
	c.History = append(c.History, map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "what": what, "from": from, "to": to})
	if len(c.History) > 200 {
		c.History = c.History[len(c.History)-200:]
	}
	saveJSON("control.json", c)
}

func (c *Check) fresh(cfg Config) bool {
	if c == nil || !c.OK || c.Account != cfg.AccountID {
		return false
	}
	t, err := time.Parse(time.RFC3339, c.At)
	return err == nil && time.Since(t) < CheckMaxMin*time.Minute
}

func status() map[string]any {
	cfg, c := loadConfig(), loadControl()
	var log []map[string]any
	loadJSON("exec_log.json", &log)
	runs := []map[string]any{}
	for i := len(log) - 1; i >= 0 && len(runs) < 15; i-- {
		runs = append(runs, log[i])
	}
	hist := []map[string]any{}
	for i := len(c.History) - 1; i >= 0 && len(hist) < 20; i-- {
		hist = append(hist, c.History[i])
	}
	sb, lv := loadState(false), loadState(true)
	cur := sb
	if cfg.Mode == "live" {
		cur = lv
	}
	// Holdings of the REAL account from the last check, for handing stocks over to the strategy.
	holdings := []map[string]any{}
	if c.Check != nil && c.Check.Plan != nil {
		d := cachedDecision()
		for t := range c.Check.Plan.Positions {
			inDecision := d != nil && d.Weights[t] > 0
			holdings = append(holdings, map[string]any{"ticker": t, "qty": c.Check.Plan.Positions[t].Qty, "in_decision": inDecision,
				"bought_by_client": contains(lv.Traded, t), "handed_over": contains(cfg.ManagedExtra, t)})
		}
		sort.Slice(holdings, func(i, j int) bool { return holdings[i]["ticker"].(string) < holdings[j]["ticker"].(string) })
	}
	return map[string]any{"version": Version, "executor": ExecutorPolicy, "mode": cfg.Mode, "stop": cfg.Stop, "auto_execute": cfg.AutoExecute,
		"account_id": cfg.AccountID, "max_order_rub": cfg.MaxOrderRub, "server_url": cfg.ServerURL, "managed_extra": cfg.ManagedExtra,
		"trade_token": hasToken(), "server_key": readSecret("server-api-key.txt") != "", "decision": cachedDecision(),
		"pending": cur.Pending, "sandbox": map[string]any{"account": sb.Account, "opened": sb.Opened, "paid_in": sb.PaidIn, "last_decision": sb.LastDecision},
		"live": map[string]any{"last_decision": lv.LastDecision, "traded": lv.Traded}, "check": c.Check, "check_fresh": c.Check.fresh(cfg),
		"holdings": holdings, "phrase": Phrase, "runs": runs, "history": hist, "last_pass": c.LastPass,
		"window": "будни, 10:05–18:30 МСК, проверка каждые 5 минут; одно решение исполняется один раз"}
}

type apiError struct {
	code int
	msg  string
}

func fail(code int, f string, a ...any) *apiError { return &apiError{code, fmt.Sprintf(f, a...)} }

type handler func(r *http.Request) (any, *apiError)

func hostOK(r *http.Request) bool {
	if allowWideHost {
		return true
	}
	h, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		h = r.Host
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}

func wrap(method string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !hostOK(r) {
			http.Error(w, "forbidden host", 403)
			return
		}
		if r.Method != method {
			http.Error(w, "method not allowed", 405)
			return
		}
		if method == "POST" && (r.Header.Get("X-Stock-Client") != "1" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") ||
			r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			writeJSON(w, 403, map[string]any{"detail": "Запрос должен исходить из приложения"})
			return
		}
		out, e := h(r)
		if e != nil {
			writeJSON(w, e.code, map[string]any{"detail": e.msg})
			return
		}
		writeJSON(w, 200, out)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func body(r *http.Request, v any) *apiError {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(v); err != nil {
		return fail(422, "Неверный запрос")
	}
	return nil
}

// updateConfig changes the config under the lock; f may refuse the change.
func updateConfig(f func(c *Config) *apiError) *apiError {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	c := loadConfig()
	if e := f(&c); e != nil {
		return e
	}
	if err := saveJSON("config.json", c); err != nil {
		return fail(500, "не удалось сохранить настройку")
	}
	return nil
}

func withResult(msg string) map[string]any { s := status(); s["result"] = msg; return s }

var (
	tokenRe   = regexp.MustCompile(tokenRegex)
	urlRe     = regexp.MustCompile(`^https?://[A-Za-z0-9.\-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~\-/]*)?$`)
	accountRe = regexp.MustCompile(`^[0-9]{1,30}$`)
)

func routes() *http.ServeMux {
	mux := http.NewServeMux()
	page := func(name, ctype string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !hostOK(r) || (name == "web/index.html" && r.URL.Path != "/") {
				http.NotFound(w, r)
				return
			}
			b, _ := webFS.ReadFile(name)
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
			w.Write(b)
		}
	}
	mux.HandleFunc("/", page("web/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("/app.js", page("web/app.js", "text/javascript; charset=utf-8"))

	mux.HandleFunc("/api/status", wrap("GET", func(r *http.Request) (any, *apiError) { return status(), nil }))

	mux.HandleFunc("/api/decision/refresh", wrap("POST", func(r *http.Request) (any, *apiError) {
		if _, err := fetchDecision(); err != nil {
			return nil, fail(502, "%v", err)
		}
		return status(), nil
	}))

	// Account check: read the REAL account and show the orders the current decision would post. Posts nothing.
	mux.HandleFunc("/api/check", wrap("POST", func(r *http.Request) (any, *apiError) {
		if !hasToken() {
			return nil, fail(409, "Сначала сохраните токен T-Invest")
		}
		cfg := loadConfig()
		p := buildPlan(cfg, true, true)
		ck := &Check{At: time.Now().UTC().Format(time.RFC3339), OK: p.Result == "", Account: p.Account, Reason: p.Result, Plan: &p}
		if ck.Account == "" {
			ck.Account = cfg.AccountID
		}
		cfgMu.Lock()
		c := loadControl()
		c.Check = ck
		saveJSON("control.json", c)
		cfgMu.Unlock()
		return status(), nil
	}))

	// Plan preview for the current mode, right now. Posts nothing.
	mux.HandleFunc("/api/plan", wrap("POST", func(r *http.Request) (any, *apiError) {
		cfg := loadConfig()
		if cfg.Mode != "sandbox" && cfg.Mode != "live" {
			return nil, fail(409, "Включите песочницу или реальный счёт")
		}
		p := buildPlan(cfg, cfg.Mode == "live", false)
		if p.Result != "" {
			return withResult(p.Result), nil
		}
		st := loadState(cfg.Mode == "live")
		st.Pending = &p
		saveJSON(stateName(cfg.Mode == "live"), st)
		return withResult(fmt.Sprintf("план: %d заявок", len(p.Orders))), nil
	}))

	// "Yes, execute": the user's click.
	mux.HandleFunc("/api/confirm", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			DecisionID string `json:"decision_id"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		msg, err := confirm(b.DecisionID)
		if err != nil {
			return nil, fail(409, "%v", err)
		}
		change("confirm", nil, msg)
		return withResult(msg), nil
	}))

	mux.HandleFunc("/api/mode", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct{ Mode, Confirm string }
		if e := body(r, &b); e != nil {
			return nil, e
		}
		var before string
		e := updateConfig(func(c *Config) *apiError {
			before = c.Mode
			switch b.Mode {
			case "off", "sandbox", "live":
			default:
				return fail(422, "Режим: off, sandbox или live")
			}
			if b.Mode == "live" {
				if strings.ToUpper(strings.TrimSpace(b.Confirm)) != Phrase {
					return fail(409, "Чтобы включить реальный счёт, введите фразу «%s»", Phrase)
				}
				if !loadControl().Check.fresh(*c) {
					return fail(409, "Сначала проверьте счёт (не раньше чем за %d минут до включения)", CheckMaxMin)
				}
			}
			if b.Mode != "off" && c.AccountID == "" {
				return fail(409, "В настройке нет номера счёта")
			}
			c.Mode = b.Mode
			return nil
		})
		if e != nil {
			return nil, e
		}
		change("mode", before, b.Mode)
		return status(), nil
	}))

	mux.HandleFunc("/api/auto", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			Auto    bool   `json:"auto"`
			Confirm string `json:"confirm"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		e := updateConfig(func(c *Config) *apiError {
			if b.Auto && c.Mode == "live" && strings.ToUpper(strings.TrimSpace(b.Confirm)) != Phrase {
				return fail(409, "Реальный счёт: чтобы включить автоисполнение, введите фразу «%s»", Phrase)
			}
			c.AutoExecute = b.Auto
			return nil
		})
		if e != nil {
			return nil, e
		}
		change("auto_execute", !b.Auto, b.Auto)
		return status(), nil
	}))

	mux.HandleFunc("/api/stop", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			Stop    bool   `json:"stop"`
			Confirm string `json:"confirm"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		e := updateConfig(func(c *Config) *apiError {
			if !b.Stop && c.Mode == "live" && strings.ToUpper(strings.TrimSpace(b.Confirm)) != Phrase {
				return fail(409, "Реальный счёт: чтобы снять остановку, введите фразу «%s»", Phrase)
			}
			c.Stop = b.Stop
			return nil
		})
		if e != nil {
			return nil, e
		}
		change("stop", !b.Stop, b.Stop)
		return status(), nil
	}))

	mux.HandleFunc("/api/limit", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			Max float64 `json:"max_order_rub"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		if b.Max < 1000 || b.Max > 1_000_000 {
			return nil, fail(422, "Предел одной заявки: от 1 000 до 1 000 000 ₽")
		}
		if e := updateConfig(func(c *Config) *apiError { c.MaxOrderRub = float64(int(b.Max)); return nil }); e != nil {
			return nil, e
		}
		change("max_order_rub", nil, b.Max)
		return status(), nil
	}))

	// Holdings handed over to the strategy (it may then sell them when they are not in the decision).
	mux.HandleFunc("/api/managed", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			Tickers []string `json:"tickers"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		for _, t := range b.Tickers {
			if !tickerRe.MatchString(t) {
				return nil, fail(422, "Неверный тикер %q", t)
			}
		}
		sort.Strings(b.Tickers)
		var before []string
		if e := updateConfig(func(c *Config) *apiError { before, c.ManagedExtra = c.ManagedExtra, b.Tickers; return nil }); e != nil {
			return nil, e
		}
		change("managed_extra", before, b.Tickers)
		return status(), nil
	}))

	// Paste the T-Invest token: checked with the broker first, then saved to secrets/tinvest-trade.txt. Never returned.
	mux.HandleFunc("/api/token", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			Token string `json:"token"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		t := strings.TrimSpace(b.Token)
		if !tokenRe.MatchString(t) {
			return nil, fail(422, "Это не похоже на токен T-Invest (латинские буквы, цифры, «.», «-», «_», от 20 символов)")
		}
		cfg := loadConfig()
		if cfg.Mode != "off" && !cfg.Stop {
			return nil, fail(409, "Сначала выключите исполнение или нажмите «Аварийная остановка»")
		}
		seen, err := getAccounts(t)
		if err != nil {
			return nil, fail(400, "Брокер не принял токен: %v", err)
		}
		execMu.Lock()
		err = writeSecret("tinvest-trade.txt", t)
		execMu.Unlock()
		if err != nil {
			return nil, fail(500, "Не удалось сохранить токен")
		}
		note := "номер счёта в настройке не задан"
		if cfg.AccountID != "" {
			note = "к счёту " + cfg.AccountID + " нет полного доступа — торговать им нельзя"
			for _, a := range seen {
				if a.ID == cfg.AccountID && a.tradable() {
					note = "полный доступ к счёту " + a.ID
				}
			}
		}
		change("token", nil, fmt.Sprintf("заменён; видно счетов: %d; %s", len(seen), note))
		return withResult(fmt.Sprintf("Токен сохранён. Видно счетов: %d; %s.", len(seen), note)), nil
	}))

	// Settings: the strategy server address and the brokerage account to trade. Changed only while execution is off or stopped.
	mux.HandleFunc("/api/settings", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			ServerURL *string `json:"server_url"`
			AccountID *string `json:"account_id"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		var before Config
		e := updateConfig(func(c *Config) *apiError {
			before = *c
			if c.Mode != "off" && !c.Stop {
				return fail(409, "Сначала выключите исполнение или нажмите «Аварийная остановка»")
			}
			if b.ServerURL != nil {
				u := strings.TrimRight(strings.TrimSpace(*b.ServerURL), "/")
				if u != "registry" && !urlRe.MatchString(u) {
					return fail(422, "Адрес сервера: https://… (или http://… внутри своей сети)")
				}
				c.ServerURL = u
			}
			if b.AccountID != nil {
				a := strings.TrimSpace(*b.AccountID)
				if a != "" && !accountRe.MatchString(a) {
					return fail(422, "Номер счёта — только цифры")
				}
				if a != c.AccountID {
					c.AccountID = a
				}
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
		cfg := loadConfig()
		change("settings", map[string]any{"server_url": before.ServerURL, "account_id": before.AccountID}, map[string]any{"server_url": cfg.ServerURL, "account_id": cfg.AccountID})
		return status(), nil
	}))

	// The key for the strategy server (issued to the user by the server's owner). Saved to secrets/server-api-key.txt, never returned.
	mux.HandleFunc("/api/server-key", wrap("POST", func(r *http.Request) (any, *apiError) {
		var b struct {
			Key string `json:"key"`
		}
		if e := body(r, &b); e != nil {
			return nil, e
		}
		k := strings.TrimSpace(b.Key)
		if !tokenRe.MatchString(k) {
			return nil, fail(422, "Ключ сервера: латинские буквы, цифры, «.», «-», «_», от 20 символов")
		}
		if err := writeSecret("server-api-key.txt", k); err != nil {
			return nil, fail(500, "Не удалось сохранить ключ")
		}
		change("server_key", nil, "заменён")
		return withResult("Ключ сервера сохранён."), nil
	}))

	// Accounts the saved token sees (id, name, type, access) — to choose the account to trade. Reads only.
	mux.HandleFunc("/api/accounts", wrap("POST", func(r *http.Request) (any, *apiError) {
		if !hasToken() {
			return nil, fail(409, "Сначала сохраните токен T-Invest")
		}
		seen, err := getAccounts("")
		if err != nil {
			return nil, fail(502, "%v", err)
		}
		out := []map[string]any{}
		for _, a := range seen {
			out = append(out, map[string]any{"id": a.ID, "name": a.Name, "type": a.Type, "tradable": a.tradable()})
		}
		return map[string]any{"accounts": out}, nil
	}))

	// A pass right now — sandbox only (the real account runs on the schedule or by the user's confirmation).
	mux.HandleFunc("/api/run", wrap("POST", func(r *http.Request) (any, *apiError) {
		if loadConfig().Mode != "sandbox" {
			return nil, fail(409, "Проход вручную — только в режиме песочницы")
		}
		return withResult(recordPass()), nil
	}))
	return mux
}

func recordPass() string {
	msg := pass()
	cfgMu.Lock()
	c := loadControl()
	c.LastPass = map[string]any{"at": time.Now().UTC().Format(time.RFC3339), "result": msg}
	saveJSON("control.json", c)
	cfgMu.Unlock()
	return msg
}
