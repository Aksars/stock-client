package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func req(mux *http.ServeMux, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "localhost:8240"
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

var app = map[string]string{"Content-Type": "application/json", "X-Stock-Client": "1"}

func TestLocalAPIGuards(t *testing.T) {
	homeDir = t.TempDir()
	mux := routes()
	if w := req(mux, "POST", "/api/stop", `{"stop":true}`, map[string]string{"Content-Type": "application/json"}); w.Code != 403 {
		t.Fatal("POST without the client header accepted")
	}
	if w := req(mux, "POST", "/api/stop", `{"stop":true}`, map[string]string{"X-Stock-Client": "1", "Content-Type": "text/plain"}); w.Code != 403 {
		t.Fatal("non-JSON POST accepted")
	}
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.Host = "evil.example:8240"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("foreign Host accepted (DNS rebinding)")
	}
	if w := req(mux, "POST", "/api/stop", `{"stop":true}`, app); w.Code != 200 || !loadConfig().Stop {
		t.Fatal("stop failed")
	}
}

func TestLiveNeedsPhraseAndFreshCheck(t *testing.T) {
	homeDir = t.TempDir()
	saveJSON("config.json", Config{Mode: "sandbox", AccountID: "42", MaxOrderRub: 1e5})
	mux := routes()
	if w := req(mux, "POST", "/api/mode", `{"mode":"live","confirm":"нет"}`, app); w.Code != 409 {
		t.Fatal("live without the phrase")
	}
	if w := req(mux, "POST", "/api/mode", `{"mode":"live","confirm":"реальный счёт"}`, app); w.Code != 409 || !strings.Contains(w.Body.String(), "проверьте") {
		t.Fatal("live without a check")
	}
	saveJSON("control.json", Control{Check: &Check{At: time.Now().UTC().Format(time.RFC3339), OK: true, Account: "42"}})
	if w := req(mux, "POST", "/api/mode", `{"mode":"live","confirm":"реальный счёт"}`, app); w.Code != 200 || loadConfig().Mode != "live" {
		t.Fatal("live refused after a fresh check")
	}
	if w := req(mux, "POST", "/api/auto", `{"auto":true}`, app); w.Code != 409 || loadConfig().AutoExecute {
		t.Fatal("auto on the real account without the phrase")
	}
}

func TestAutoIsOffByDefault(t *testing.T) {
	homeDir = t.TempDir()
	if loadConfig().AutoExecute || loadConfig().Mode != "off" {
		t.Fatal("defaults must be off")
	}
}

func TestTokenCheckedSavedNeverReturned(t *testing.T) {
	homeDir = t.TempDir()
	saveJSON("config.json", Config{Mode: "off", AccountID: "42"})
	good := "t." + strings.Repeat("a", 40)
	old := getAccounts
	getAccounts = func(tok string) ([]Account, error) {
		if tok != good {
			return nil, &BrokerError{"T-Invest HTTP 401"}
		}
		return []Account{{ID: "42", Status: "ACCOUNT_STATUS_OPEN", Access: "ACCOUNT_ACCESS_LEVEL_FULL_ACCESS"}}, nil
	}
	t.Cleanup(func() { getAccounts = old })
	mux := routes()
	if w := req(mux, "POST", "/api/token", `{"token":"short"}`, app); w.Code != 422 {
		t.Fatal("bad format accepted")
	}
	if w := req(mux, "POST", "/api/token", `{"token":"t.`+strings.Repeat("b", 40)+`"}`, app); w.Code != 400 || hasToken() {
		t.Fatal("rejected token saved")
	}
	w := req(mux, "POST", "/api/token", `{"token":"  `+good+`  "}`, app)
	if w.Code != 200 || strings.Contains(w.Body.String(), good) || !strings.Contains(w.Body.String(), "полный доступ к счёту 42") {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	if readSecret("tinvest-trade.txt") != good {
		t.Fatal("token not saved")
	}
	if fi, _ := os.Stat(secretPath("tinvest-trade.txt")); fi.Mode().Perm() != 0o600 {
		t.Fatal("token file is not owner-only")
	}
	if w := req(mux, "GET", "/api/status", "", nil); strings.Contains(w.Body.String(), good) {
		t.Fatal("token in status")
	}
	saveJSON("config.json", Config{Mode: "sandbox", AccountID: "42"})
	if w := req(mux, "POST", "/api/token", `{"token":"`+good+`"}`, app); w.Code != 409 {
		t.Fatal("token replaced while executing")
	}
}

func TestWindow(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, msk) }
	if !inWindow(at(23, 10, 5)) || inWindow(at(23, 10, 4)) || inWindow(at(23, 18, 31)) || inWindow(at(21, 12, 0)) {
		t.Fatal("window")
	}
}

func TestSettingsAndServerKey(t *testing.T) {
	homeDir = t.TempDir()
	mux := routes()
	if w := req(mux, "POST", "/api/settings", `{"server_url":"javascript:alert(1)"}`, app); w.Code != 422 {
		t.Fatal("bad url accepted")
	}
	if w := req(mux, "POST", "/api/settings", `{"server_url":"https://strategy.example.com/","account_id":"123"}`, app); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if c := loadConfig(); c.ServerURL != "https://strategy.example.com" || c.AccountID != "123" {
		t.Fatalf("%+v", c)
	}
	if w := req(mux, "POST", "/api/settings", `{"account_id":"12a"}`, app); w.Code != 422 {
		t.Fatal("bad account accepted")
	}
	key := strings.Repeat("k", 43)
	w := req(mux, "POST", "/api/server-key", `{"key":"`+key+`"}`, app)
	if w.Code != 200 || strings.Contains(w.Body.String(), key) || readSecret("server-api-key.txt") != key {
		t.Fatal("server key")
	}
	saveJSON("config.json", Config{Mode: "sandbox", AccountID: "123"})
	if w := req(mux, "POST", "/api/settings", `{"account_id":"9"}`, app); w.Code != 409 {
		t.Fatal("settings changed while executing")
	}
}

func TestNoServerAddressByDefault(t *testing.T) {
	homeDir = t.TempDir()
	if _, err := serverURL(loadConfig()); err == nil {
		t.Fatal("a default server address must not exist")
	}
}

func TestListenFallsBackToTheNextPort(t *testing.T) {
	a, err := listenFrom("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	busy := a.Addr().String()
	b, err := listenFrom(busy)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Addr().String() == busy {
		t.Fatal("same port")
	}
}
