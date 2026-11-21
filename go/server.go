// The strategy server. The client makes exactly three kinds of request to it:
//
//	GET  {server_url}/api/v1/decision   header Authorization: Bearer <secrets/server-api-key.txt>
//	POST {server_url}/api/v1/login      {login, password, device} — only when the user presses «Войти»; answer: a device key
//	POST {server_url}/api/v1/logout     header Authorization — only when the user presses «Выйти»
//
// Nothing else is ever sent to the server: no T-Invest token, no account number, no positions, no orders, no logs.
// The password is sent once at login and is not stored. Login over plain http is refused unless the server is on this computer or
// in a private network.
// Every decision must carry an Ed25519 signature by a key in TrustedKeys; only the signed bytes are used.
// The answer (contract client-decision-v1) holds only target weights and dates; it is checked by validateDecision
// before anything is done with it, so even a broken or hostile server cannot make the client trade outside the
// guards below.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const Contract = "client-decision-v1"

// Guards against a bad decision (also protect the user from a compromised server).
var (
	CashFunds      = []string{"TPAY"}                     // the only funds accepted as the cash part
	tickerRe       = regexp.MustCompile(`^[A-Z0-9]{1,12}$`) // exchange tickers only
	MaxStocks      = 30                                   // more names than this is not a plausible decision
	MaxStockWeight = 0.50                                 // one stock may not be above half of the portfolio
)

// TrustedKeys: public keys of the strategy server (key id -> base64). A decision is accepted only with a valid signature by one
// of them. The private key never leaves the server (secrets/client-signing-key.json there).
var TrustedKeys = map[string]string{
	"fc3fc737": "iwsWRQ7IGlBZe+ntoaM9P2OR9ks/QA4Mn84B9mWkUIs=",
}

type signature struct {
	Alg     string `json:"alg"`
	KeyID   string `json:"key_id"`
	Payload string `json:"payload"`
	Sig     string `json:"sig"`
}

// parseDecision checks the signature and returns the decision made of the SIGNED payload only (unsigned fields are ignored).
func parseDecision(raw []byte) (*Decision, error) {
	var env struct {
		Signature *signature `json:"signature"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, errors.New("server answer is not a decision")
	}
	body := raw
	if len(TrustedKeys) > 0 {
		sg := env.Signature
		if sg == nil {
			return nil, errors.New("decision is not signed")
		}
		pk, ok := TrustedKeys[sg.KeyID]
		if !ok || sg.Alg != "ed25519" {
			return nil, fmt.Errorf("decision signed by an unknown key %q", sg.KeyID)
		}
		pub, _ := base64.StdEncoding.DecodeString(pk)
		payload, err1 := base64.StdEncoding.DecodeString(sg.Payload)
		sig, err2 := base64.StdEncoding.DecodeString(sg.Sig)
		if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, payload, sig) {
			return nil, errors.New("decision signature is not valid")
		}
		body = payload
	}
	var d Decision
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, errors.New("signed payload is not a decision")
	}
	if err := validateDecision(&d); err != nil {
		return nil, fmt.Errorf("decision rejected: %v", err)
	}
	return &d, nil
}

type Decision struct {
	Contract     string             `json:"contract"`
	Policy       string             `json:"policy"`
	DecisionDate string             `json:"decision_date"`
	ExecuteOn    string             `json:"execute_on"`
	ValidUntil   string             `json:"valid_until"`
	CashTicker   string             `json:"cash_ticker"`
	Weights      map[string]float64 `json:"weights"`
	DecisionID   string             `json:"decision_id"`
	PublishedAt  string             `json:"published_at"`
	FetchedAt    string             `json:"fetched_at,omitempty"` // set by the client
}

func validateDecision(d *Decision) error {
	if d.Contract != Contract {
		return fmt.Errorf("unknown contract %q", d.Contract)
	}
	if len(d.Weights) == 0 {
		return errors.New("no weights")
	}
	if !contains(CashFunds, d.CashTicker) {
		return fmt.Errorf("cash ticker %q is not an allowed fund", d.CashTicker)
	}
	sum, n := 0.0, 0
	for t, w := range d.Weights {
		if !tickerRe.MatchString(t) {
			return fmt.Errorf("bad ticker %q", t)
		}
		if w < 0 || math.IsNaN(w) || w > 1 {
			return fmt.Errorf("bad weight of %s", t)
		}
		if t != d.CashTicker {
			n++
			if w > MaxStockWeight {
				return fmt.Errorf("%s: %.0f%% of the portfolio in one stock is above the client's limit", t, 100*w)
			}
		}
		sum += w
	}
	if math.Abs(sum-1) > 0.01 {
		return fmt.Errorf("weights sum to %.4f, not 1", sum)
	}
	if n > MaxStocks {
		return fmt.Errorf("%d stocks is above the client's limit", n)
	}
	if d.DecisionID == "" {
		return errors.New("no decision_id")
	}
	for _, s := range []string{d.DecisionDate, d.ValidUntil} {
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return fmt.Errorf("bad date %q", s)
		}
	}
	return nil
}

func expired(d *Decision, today string) bool { return today > d.ValidUntil }

// RegistryFile: development only. With server_url = "registry" the address of the strategy server is read from this
// file, which the server writes at start in the shared registry of the sandboxes (its container IP may change after a
// restart). It holds only a URL. A user's client has a fixed server address instead.
var RegistryFile = "registry/stock-analysis/client-api.json"

func serverURL(cfg Config) (string, error) {
	if cfg.ServerURL != "registry" {
		if cfg.ServerURL == "" {
			return "", errors.New("server address is not set (Settings)")
		}
		return cfg.ServerURL, nil
	}
	var e struct {
		URL string `json:"url"`
	}
	b, err := os.ReadFile(filepath.Join(homeDir, RegistryFile))
	if err != nil || json.Unmarshal(b, &e) != nil || !strings.HasPrefix(e.URL, "http://") {
		return "", errors.New("server address not found in the registry")
	}
	return e.URL, nil
}

// fetchDecision is a variable so that tests can replace it.
var fetchDecision = func() (*Decision, error) {
	cfg := loadConfig()
	base, err := serverURL(cfg)
	if err != nil {
		return nil, err
	}
	key := readSecret("server-api-key.txt")
	if key == "" {
		return nil, errors.New("no server key (secrets/server-api-key.txt)")
	}
	req, err := http.NewRequest("GET", strings.TrimRight(base, "/")+"/api/v1/decision", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }} // no redirects: the key goes to the configured address only
	resp, err := c.Do(req)
	if err != nil {
		return nil, errors.New("server unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("server HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("server answer was cut")
	}
	d, err := parseDecision(raw)
	if err != nil {
		return nil, err
	}
	d.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	saveJSON("decision_cache.json", cacheEntry{FetchedAt: d.FetchedAt, Response: raw})
	return d, nil
}

// cacheEntry keeps the server's answer as received, so that the cached decision is verified again before any use.
type cacheEntry struct {
	FetchedAt string          `json:"fetched_at"`
	Response  json.RawMessage `json:"response"`
}

func cachedDecision() *Decision {
	var c cacheEntry
	if !loadJSON("decision_cache.json", &c) || len(c.Response) == 0 {
		return nil
	}
	d, err := parseDecision(c.Response)
	if err != nil {
		return nil
	}
	d.FetchedAt = c.FetchedAt
	return d
}

// loginAllowed: the password may travel over plain http only to this computer or a private network address.
func loginAllowed(base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return errors.New("bad server address")
	}
	if u.Scheme == "https" {
		return nil
	}
	h := u.Hostname()
	if ip := net.ParseIP(h); h == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsPrivate())) {
		return nil
	}
	return errors.New("вход по http разрешён только к серверу на этом компьютере или в локальной сети — нужен https://")
}

// serverPost sends one JSON request to the strategy server (login / logout only).
func serverPost(base, path, key string, body any) (map[string]any, int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	c := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, errors.New("server unreachable")
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	return out, resp.StatusCode, nil
}
