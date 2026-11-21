package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func dec() *Decision {
	return &Decision{Contract: Contract, Policy: "p", DecisionDate: "2026-11-20", ValidUntil: "2026-11-24", CashTicker: "TPAY",
		Weights: map[string]float64{"X5": 0.45, "LKOH": 0.30, "TPAY": 0.25}, DecisionID: "abc"}
}

func TestValidateDecision(t *testing.T) {
	if err := validateDecision(dec()); err != nil {
		t.Fatal(err)
	}
	bad := []func(d *Decision){
		func(d *Decision) { d.Contract = "v0" },
		func(d *Decision) { d.Weights = map[string]float64{"X5": 0.5} },
		func(d *Decision) { d.Weights = map[string]float64{} },
		func(d *Decision) { d.Weights = map[string]float64{"X5": 1.2, "TPAY": -0.2} },
		func(d *Decision) { d.CashTicker = "SBMM" },                                     // not an allowed fund
		func(d *Decision) { d.Weights = map[string]float64{"X5": 0.8, "TPAY": 0.2} },    // one stock above 50%
		func(d *Decision) { d.Weights = map[string]float64{"x5;rm": 0.5, "TPAY": 0.5} }, // not a ticker
		func(d *Decision) { d.ValidUntil = "soon" },
		func(d *Decision) { d.DecisionID = "" },
	}
	for i, f := range bad {
		d := dec()
		f(d)
		if validateDecision(d) == nil {
			t.Fatalf("bad decision %d accepted", i)
		}
	}
}

func withFetch(t *testing.T, d *Decision, err error) {
	old := fetchDecision
	fetchDecision = func() (*Decision, error) { return d, err }
	t.Cleanup(func() { fetchDecision = old })
}

func TestPickDecision(t *testing.T) {
	homeDir = t.TempDir()
	d := dec()
	withFetch(t, d, nil)
	if x, _, _ := pickDecision(State{}, 0, 100, "2026-11-21"); x != d {
		t.Fatal("new decision not taken")
	}
	if x, _, n := pickDecision(State{LastDecision: "abc"}, 1, 100, "2026-11-21"); x != nil || n == "" {
		t.Fatal("executed twice")
	}
	if x, top, _ := pickDecision(State{LastDecision: "abc"}, 10, 100, "2026-11-27"); x != d || !top {
		t.Fatal("new money not invested")
	}
	if x, _, _ := pickDecision(State{LastDecision: "abc"}, 10, 100, "2026-11-29"); x != nil {
		t.Fatal("new money by a stale decision")
	}
	if x, _, _ := pickDecision(State{}, 0, 100, "2026-11-25"); x != nil {
		t.Fatal("expired decision executed")
	}
}

func TestServerDownOnlyTopUpByCache(t *testing.T) {
	homeDir = t.TempDir()
	useTestKey(t)
	saveJSON("decision_cache.json", cacheEntry{FetchedAt: "x", Response: signedRaw(dec(), testKey)})
	withFetch(t, nil, errors.New("down"))
	if x, _, _ := pickDecision(State{}, 0, 100, "2026-11-21"); x != nil {
		t.Fatal("cached decision executed as new")
	}
	if x, _, _ := pickDecision(State{LastDecision: "abc"}, 10, 100, "2026-11-22"); x == nil {
		t.Fatal("top-up by the cached decision refused")
	}
}

func TestManagedSet(t *testing.T) {
	m := managed(dec(), State{Traded: []string{"T"}}, Config{ManagedExtra: []string{"BELU"}})
	for _, x := range []string{"X5", "LKOH", "TPAY", "T", "BELU"} {
		if !m[x] {
			t.Fatal(x)
		}
	}
	if m["TMOS"] {
		t.Fatal("foreign holding managed")
	}
}

func TestServerURLFromRegistry(t *testing.T) {
	homeDir = t.TempDir()
	if u, _ := serverURL(Config{ServerURL: "http://x:1"}); u != "http://x:1" {
		t.Fatal("fixed address changed")
	}
	if _, err := serverURL(Config{ServerURL: "registry"}); err == nil {
		t.Fatal("missing registry file accepted")
	}
	os.MkdirAll(filepath.Join(homeDir, "registry", "stock-analysis"), 0o700)
	os.WriteFile(filepath.Join(homeDir, RegistryFile), []byte(`{"url":"http://10.0.0.1:8001","api":"client-api-v1"}`), 0o600)
	if u, err := serverURL(Config{ServerURL: "registry"}); err != nil || u != "http://10.0.0.1:8001" {
		t.Fatal(u, err)
	}
}
