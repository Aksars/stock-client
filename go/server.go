// The strategy server. The client makes exactly ONE kind of request to it:
//
//	GET {server_url}/api/v1/decision   with the header  Authorization: Bearer <secrets/server-api-key.txt>
//
// Nothing else is ever sent to the server: no token, no account number, no positions, no orders, no logs.
// The answer (contract client-decision-v1) holds only target weights and dates; it is checked by validateDecision
// before anything is done with it, so even a broken or hostile server cannot make the client trade outside the
// guards below.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
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
	var d Decision
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, errors.New("server answer is not a decision")
	}
	if err := validateDecision(&d); err != nil {
		return nil, fmt.Errorf("decision rejected: %v", err)
	}
	d.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	saveJSON("decision_cache.json", d)
	return &d, nil
}

func cachedDecision() *Decision {
	var d Decision
	if !loadJSON("decision_cache.json", &d) || validateDecision(&d) != nil {
		return nil
	}
	return &d
}
