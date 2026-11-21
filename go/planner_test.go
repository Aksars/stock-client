package main

import (
	"strings"
	"testing"
)

func info(px float64, lot int, have float64) Info { return Info{UID: "u", Lot: lot, Px: px, Step: 0.01, Have: have} }

var t75 = map[string]float64{"LKOH": 0.3375, "X5": 0.3375, "T": 0.075, "TPAY": 0.25}

func after(orders []Order, inf map[string]Info, total float64) map[string]float64 {
	have := map[string]float64{}
	for t, x := range inf {
		have[t] = x.Have
	}
	for _, o := range orders {
		s := 1.0
		if o.Side == "sell" {
			s = -1
		}
		have[o.Ticker] += s * float64(o.Lots*o.Lot)
	}
	w := map[string]float64{}
	for t := range inf {
		w[t] = have[t] * inf[t].Px / total
	}
	return w
}

// The server's old fallback capped stocks at 54.5% when risk.eq was missing; with 75% stocks the client must reach ~75%.
func TestStockCapFollowsTheTarget(t *testing.T) {
	inf := map[string]Info{"LKOH": info(7000, 1, 0), "X5": info(3000, 1, 0), "T": info(3000, 1, 0), "TPAY": info(100, 1, 0)}
	o, skip := planOrders(t75, inf, 1e6, 1e6, "TPAY", false)
	w := after(o, inf, 1e6)
	st := w["LKOH"] + w["X5"] + w["T"]
	if !(st > 0.73 && st <= 0.795+1e-9) || w["TPAY"] < 0.2 || len(skip) > 0 {
		t.Fatalf("stocks %.4f tpay %.4f skip %v", st, w["TPAY"], skip)
	}
	if max(w["LKOH"], w["X5"], w["T"]) > 0.5*max(st, 0.75)+1e-9 {
		t.Fatal("one stock above half of the stock part")
	}
	sum := 0.0
	for _, x := range w {
		sum += x
	}
	if sum > 1-Buffer+1e-9 {
		t.Fatal("buffer not kept")
	}
}

func TestExpensiveLotNoOvershoot(t *testing.T) {
	inf := map[string]Info{"GMKN": info(120000, 1, 0), "TPAY": info(100, 1, 0)}
	o, _ := planOrders(map[string]float64{"GMKN": 0.25, "TPAY": 0.75}, inf, 3e5, 3e5, "TPAY", false)
	if after(o, inf, 3e5)["GMKN"] > 0.295+1e-9 {
		t.Fatal("overshoot")
	}
}

func TestSellsFirstBuysCutToCash(t *testing.T) {
	inf := map[string]Info{"LKOH": info(7000, 1, 20), "X5": info(3000, 1, 0), "TPAY": info(100, 1, 0)}
	o, _ := planOrders(map[string]float64{"X5": 0.3, "TPAY": 0.7}, inf, 1e5, 1e6, "TPAY", true)
	if len(o) == 0 || o[0].Side != "sell" || o[0].Ticker != "LKOH" {
		t.Fatalf("%v", o)
	}
	spent := 0.0
	for _, x := range o {
		if x.Side == "buy" {
			spent += x.Value * (1 + Slip)
		}
	}
	if spent > 1e5+140000+1 {
		t.Fatal("buys exceed cash")
	}
}

func TestTurnoverGuard(t *testing.T) {
	inf := map[string]Info{"LKOH": info(7000, 1, 100), "X5": info(3000, 1, 0), "TPAY": info(100, 1, 0)}
	o, skip := planOrders(map[string]float64{"X5": 0.75, "TPAY": 0.25}, inf, 0, 7e5, "TPAY", true)
	if len(o) != 0 || !strings.Contains(skip[len(skip)-1], "ничего не отправлено") {
		t.Fatal("turnover not blocked")
	}
	o, skip = planOrders(map[string]float64{"X5": 0.75, "TPAY": 0.25}, map[string]Info{"X5": info(3000, 1, 0), "TPAY": info(100, 1, 0)}, 7e5, 7e5, "TPAY", false)
	if len(o) == 0 || len(skip) > 0 {
		t.Fatal("first fill blocked")
	}
}

func TestSmallChangeNotTraded(t *testing.T) {
	inf := map[string]Info{"X5": info(3000, 1, 250), "TPAY": info(100, 1, 2480)}
	o, _ := planOrders(map[string]float64{"X5": 0.75, "TPAY": 0.25}, inf, 4000, 1e6, "TPAY", true)
	for _, x := range o {
		if x.Ticker == "X5" {
			t.Fatal("small change traded")
		}
	}
}

func TestSnapAndMissingPrice(t *testing.T) {
	if snap(100.37, 0.5, true) != 100 || snap(100.37, 0.5, false) != 100.5 {
		t.Fatal("snap")
	}
	_, skip := planOrders(map[string]float64{"X5": 0.5, "TPAY": 0.5}, map[string]Info{"TPAY": info(100, 1, 0)}, 1e5, 1e5, "TPAY", false)
	if len(skip) == 0 || !strings.Contains(skip[0], "X5") {
		t.Fatal("missing price not reported")
	}
}
