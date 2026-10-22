// Package domain defines the bounded commerce overview contract.
package domain

import (
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"time"
)

var ErrValidation = errors.New("invalid overview parameters")
var ErrUnavailable = errors.New("analytics unavailable")

const MaxSafeCount uint64 = 1<<53 - 1

// Filters uses date strings on the wire and UTC half-open boundaries in SQL.
type Filters struct {
	Currency string `json:"currency"`
	From     string `json:"from"`
	To       string `json:"to"`
}

func ParseFilters(values url.Values) (Filters, error) {
	if len(values) != 3 {
		return Filters{}, ErrValidation
	}
	for key, entries := range values {
		if (key != "currency" && key != "from" && key != "to") || len(entries) != 1 {
			return Filters{}, ErrValidation
		}
	}
	f := Filters{Currency: values.Get("currency"), From: values.Get("from"), To: values.Get("to")}
	return f, f.Validate()
}
func (f Filters) Validate() error {
	from, e1 := time.Parse(time.DateOnly, f.From)
	to, e2 := time.Parse(time.DateOnly, f.To)
	if f.Currency != "USD" || e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 90*24*time.Hour {
		return ErrValidation
	}
	return nil
}
func (f Filters) Bounds() (time.Time, time.Time) {
	from, _ := time.Parse(time.DateOnly, f.From)
	to, _ := time.Parse(time.DateOnly, f.To)
	return from, to
}

// Money is a canonical base-ten integer encoded as a JSON string.
// Arithmetic never goes through float64 or a narrowed int64 aggregate.
type Money string

func Integer(value Money) (*big.Int, error) {
	n, ok := new(big.Int).SetString(string(value), 10)
	if !ok || n.String() != string(value) {
		return nil, ErrUnavailable
	}
	return n, nil
}
func Count(value string) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n > MaxSafeCount {
		return 0, ErrUnavailable
	}
	return n, nil
}

type KPI struct {
	Settled        Money  `json:"settled_capture_minor"`
	Refunded       Money  `json:"refunded_minor"`
	Net            Money  `json:"net_captured_minor"`
	CapturedOrders uint64 `json:"captured_orders"`
	Average        *Money `json:"average_captured_order_minor"`
}
type Daily struct {
	Date     string `json:"date"`
	Settled  Money  `json:"settled_capture_minor"`
	Refunded Money  `json:"refunded_minor"`
	Net      Money  `json:"net_captured_minor"`
}
type Funnel struct {
	Started   uint64 `json:"checkout_started"`
	Created   uint64 `json:"order_created"`
	Captured  uint64 `json:"payment_captured"`
	Completed uint64 `json:"order_completed"`
}
type Product struct {
	ID       string `json:"product_id"`
	Name     string `json:"product_name"`
	Quantity uint64 `json:"quantity"`
	Orders   uint64 `json:"order_count"`
	Subtotal Money  `json:"item_subtotal_minor"`
}
type Source struct {
	Name       string     `json:"name"`
	SyncedAt   *time.Time `json:"synced_at"`
	LagSeconds *float64   `json:"lag_seconds"`
	State      string     `json:"state"`
}
type Meta struct {
	GeneratedAt time.Time  `json:"generated_at"`
	DataThrough *time.Time `json:"data_through"`
	Stale       bool       `json:"stale"`
	Sources     []Source   `json:"sources"`
}
type Overview struct {
	Filters  Filters   `json:"filters"`
	KPI      KPI       `json:"kpis"`
	Trend    []Daily   `json:"trend"`
	Funnel   Funnel    `json:"funnel"`
	Products []Product `json:"top_products"`
	Meta     Meta      `json:"meta"`
}
type Heartbeat struct {
	Name string
	At   *time.Time
}
type Facts struct {
	Daily          []Daily
	CapturedOrders uint64
	Funnel         Funnel
	Products       []Product
}
