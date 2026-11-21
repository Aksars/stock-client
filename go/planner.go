// Rounding of the target weights to whole lots (planner-v1). Pure functions: no network, no files.
//
//  1. Every stock of the target is rounded DOWN to whole lots.
//  2. One lot at a time is added to the stock whose weight it brings closest to the target, while
//     - the stock part stays within min(1, stock share of the target + 5 p.p.) - 0.5% buffer, and
//     - one stock stays within 50% of the stock part (of the target stock part while filling up).
//  3. What is left goes to the cash fund (TPAY: no broker fee), keeping a 0.5% buffer.
//  4. A change is traded only if it is at least 1 lot and 0.5% of the portfolio (a full sale always goes);
//     limit price = last price +-0.3% snapped to the price step; sells go first; buys are cut to the cash
//     available after the sells; a stock turnover above 60% of the portfolio blocks the whole plan
//     (except the first fill of an account that holds only cash).
package main

import (
	"fmt"
	"math"
	"sort"
)

const (
	MaxTurn = 0.60
	MinW    = 0.005
	Slip    = 0.003
	EqAllow = 0.05
	OneMax  = 0.50
	Buffer  = 0.005
)

// Info is what the planner needs to know about one ticker.
type Info struct {
	UID  string  `json:"uid"`
	Lot  int     `json:"lot"`
	Px   float64 `json:"px"`
	Step float64 `json:"step"`
	Have float64 `json:"have"`
}

type Order struct {
	Ticker     string  `json:"ticker"`
	UID        string  `json:"uid"`
	Side       string  `json:"side"` // "buy" | "sell"
	Lots       int     `json:"lots"`
	Lot        int     `json:"lot"`
	Price      float64 `json:"price"`
	Value      float64 `json:"value"`
	OrderID    string  `json:"order_id,omitempty"`
	Status     string  `json:"status,omitempty"`
	FilledLots string  `json:"filled_lots,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// snap rounds a limit price to the step: a buy not above the limit, a sell not below it.
func snap(p, step float64, buy bool) float64 {
	var k float64
	if buy {
		k = math.Floor(p/step + 1e-9)
	} else {
		k = math.Ceil(p/step - 1e-9)
	}
	return math.Round(k*step*1e9) / 1e9
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// planOrders: target weights (sum 1), info for every ticker that may be traded, roubles and the total value.
func planOrders(target map[string]float64, info map[string]Info, cash, total float64, cashTicker string, hasPositions bool) ([]Order, []string) {
	var skip []string
	if total <= 0 {
		return nil, []string{"портфель пуст"}
	}
	eq := 0.0
	var stocks []string
	for t, w := range target {
		if t == cashTicker {
			continue
		}
		eq += w
		if w <= 0 {
			continue
		}
		if _, ok := info[t]; !ok {
			skip = append(skip, t+": нет цены или лота — пропущена")
			continue
		}
		stocks = append(stocks, t)
	}
	sort.Strings(stocks) // deterministic order
	unit := map[string]float64{}
	lots := map[string]int{}
	tgtPart := 0.0
	for _, t := range stocks {
		unit[t] = float64(info[t].Lot) * info[t].Px / total
		lots[t] = int(target[t] / unit[t])
		tgtPart += target[t]
	}
	smax := math.Min(1, eq+EqAllow) - Buffer
	w := func(t string) float64 { return float64(lots[t]) * unit[t] }
	for {
		tot := 0.0
		for _, t := range stocks {
			tot += w(t)
		}
		best, bestG := "", 0.0
		for _, t := range stocks {
			d := unit[t]
			if tot+d > smax+1e-9 || w(t)+d > OneMax*math.Max(tot+d, tgtPart)+1e-9 {
				continue
			}
			g := math.Pow(target[t]-w(t), 2) - math.Pow(target[t]-w(t)-d, 2) // closer to the target
			if g > 0 && (best == "" || g > bestG) {
				best, bestG = t, g
			}
		}
		if best == "" {
			break
		}
		lots[best]++
	}
	want := map[string]float64{}
	spent := 0.0
	for _, t := range stocks {
		want[t] = float64(lots[t] * info[t].Lot)
		spent += want[t] * info[t].Px
	}
	if c, ok := info[cashTicker]; ok {
		left := total*(1-Buffer) - spent
		want[cashTicker] = math.Max(0, math.Floor(left/(float64(c.Lot)*c.Px))) * float64(c.Lot)
	}
	var orders []Order
	tickers := make([]string, 0, len(info))
	for t := range info {
		tickers = append(tickers, t)
	}
	sort.Strings(tickers)
	for _, t := range tickers {
		x := info[t]
		n := int(math.Round((want[t] - x.Have) / float64(x.Lot)))
		if n == 0 || (math.Abs(float64(n*x.Lot)*x.Px) < MinW*total && want[t] > 0) {
			continue
		}
		buy := n > 0
		p := x.Px * (1 - Slip)
		side := "sell"
		if buy {
			p, side = x.Px*(1+Slip), "buy"
		}
		orders = append(orders, Order{Ticker: t, UID: x.UID, Side: side, Lots: abs(n), Lot: x.Lot, Price: snap(p, x.Step, buy),
			Value: round2(float64(abs(n)*x.Lot) * x.Px)})
	}
	sort.SliceStable(orders, func(i, j int) bool { return orders[i].Side == "sell" && orders[j].Side != "sell" })
	turn := 0.0
	for _, o := range orders {
		if o.Ticker != cashTicker {
			turn += o.Value
		}
	}
	turn /= total
	if turn > MaxTurn && hasPositions {
		return nil, append(skip, fmt.Sprintf("оборот %.0f%% больше предела %.0f%% — ничего не отправлено", 100*turn, 100*MaxTurn))
	}
	left := cash
	for _, o := range orders {
		if o.Side == "sell" {
			left += o.Value
		}
	}
	var out []Order
	for _, o := range orders {
		if o.Side == "buy" {
			if o.Value*(1+Slip) > left {
				o.Lots = int(math.Max(0, math.Floor(left/(o.Price*float64(o.Lot)*(1+Slip)))))
				o.Value = round2(float64(o.Lots*o.Lot) * o.Price)
			}
			left -= o.Value * (1 + Slip)
		}
		if o.Lots > 0 {
			out = append(out, o)
		}
	}
	return out, skip
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
