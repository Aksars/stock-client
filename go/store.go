// stock-client — the user's side of the strategy.
//
// What it does, in one paragraph: it asks the strategy server "what should the portfolio be now?" and gets back
// ONLY target weights (ticker -> share of the portfolio) and dates. It rounds them to whole lots against the
// user's own account and, after the user's click (or automatically, if the user turned that on), posts limit
// orders to T-Invest with the user's own token. The token and everything about the account stay on this machine.
//
// Files (everything lives under the home folder, flag -home; default: the folder of the executable):
//
//	secrets/tinvest-trade.txt   the user's T-Invest token. Read only by tinvest.go. Sent ONLY to invest-public-api.tbank.ru.
//	secrets/server-api-key.txt  the key for the strategy server. Read only by server.go. Sent ONLY to the strategy server.
//	data/config.json            settings (mode, stop, auto-execution, account, order limit, server address, handed-over stocks)
//	data/state_*.json           what was executed (per mode), the plan waiting for a click
//	data/exec_log.json          every pass with its orders; data/control.json: changes and account checks; data/app.log
//
// See AUDIT.md for the complete list of network connections and how to verify that secrets never reach the server.
// This file: paths and small JSON stores. Only the Go standard library is used anywhere in this program.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	homeDir    = "."
	cfgMu      sync.Mutex // guards config.json and control.json
	execMu     sync.Mutex // one execution pass at a time
	tokenRegex = `^[A-Za-z0-9._\-]{20,300}$`
)

func dataPath(name string) string   { return filepath.Join(homeDir, "data", name) }
func secretPath(name string) string { return filepath.Join(homeDir, "secrets", name) }

// loadJSON reads data/<name> into v; false if missing or broken (v keeps its defaults).
func loadJSON(name string, v any) bool {
	b, err := os.ReadFile(dataPath(name))
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// saveJSON writes data/<name> atomically (temporary file + rename).
func saveJSON(name string, v any) error {
	if err := os.MkdirAll(filepath.Join(homeDir, "data"), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := dataPath(name + ".pending")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dataPath(name))
}

// readSecret returns the trimmed content of secrets/<name>, "" if there is none.
func readSecret(name string) string {
	b, err := os.ReadFile(secretPath(name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeSecret stores a secret owner-only and atomically. Used only for the T-Invest token pasted in the interface.
func writeSecret(name, value string) error {
	if err := os.MkdirAll(filepath.Join(homeDir, "secrets"), 0o700); err != nil {
		return err
	}
	tmp := secretPath(name + ".pending")
	if err := os.WriteFile(tmp, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, secretPath(name))
}

// Config is data/config.json. Defaults: everything off, nothing automatic.
type Config struct {
	Mode         string   `json:"mode"`          // "off" | "sandbox" (broker's virtual account) | "live" (real orders)
	Stop         bool     `json:"stop"`          // emergency stop: nothing is sent while true
	AutoExecute  bool     `json:"auto_execute"`  // false: a new plan waits for the user's "Yes, execute" click
	AccountID    string   `json:"account_id"`    // the ONE brokerage account that may be traded
	MaxOrderRub  float64  `json:"max_order_rub"` // live: one order at most this many roubles
	ServerURL    string   `json:"server_url"`    // the strategy server
	ManagedExtra []string `json:"managed_extra"` // stocks the user already held and handed over to the strategy
}

func defaultConfig() Config {
	return Config{Mode: "off", MaxOrderRub: 100000} // server address, server key and account are set by the user in the interface
}

func loadConfig() Config {
	c := defaultConfig()
	loadJSON("config.json", &c)
	if c.Mode == "" {
		c.Mode = "off"
	}
	return c
}

// State is what the executor remembers per mode (data/state_sandbox.json, data/state_live.json).
type State struct {
	Account      string   `json:"account,omitempty"`       // sandbox: the virtual account opened by the client
	Opened       string   `json:"opened,omitempty"`        // sandbox: date it was opened
	PaidIn       float64  `json:"paid_in,omitempty"`       // sandbox: virtual roubles paid in at opening
	MirroredIn   float64  `json:"mirrored_in,omitempty"`   // sandbox: deposits to the real account already mirrored
	LastDecision string   `json:"last_decision,omitempty"` // decision_id executed last (a decision is executed once)
	Traded       []string `json:"traded,omitempty"`        // tickers the client bought by the server's decisions
	Pending      *Plan    `json:"pending,omitempty"`       // a plan waiting for the user's click (auto-execution off)
}

func stateName(live bool) string {
	if live {
		return "state_live.json"
	}
	return "state_sandbox.json"
}

func loadState(live bool) State {
	var s State
	loadJSON(stateName(live), &s)
	return s
}

func contains(list []string, x string) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}
