package domain

import (
	"net/url"
	"strings"
	"testing"
)

func TestFilters(t *testing.T) {
	tests := []struct {
		query string
		valid bool
	}{
		{"currency=USD&from=2026-01-01&to=2026-04-01", true},
		{"currency=USD&from=2026-01-01&to=2026-04-02", false},
		{"currency=USD&from=2026-02-29&to=2026-03-01", false},
		{"currency=USD&from=2024-02-29&to=2024-03-01", true},
		{"currency=USD&from=2026-01-01&to=2026-01-01", false},
		{"currency=USD&from=2026-01-02&to=2026-01-01", false},
		{"currency=VND&from=2026-01-01&to=2026-01-02", false},
		{"currency=USD&from=2026-01-01&to=2026-01-02&x=1", false},
		{"currency=USD&from=2026-01-01&to=2026-01-02&currency=USD", false},
		{"currency=USD&from=2026-01-01", false},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			v, _ := url.ParseQuery(tt.query)
			f, err := ParseFilters(v)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
			if err == nil {
				from, to := f.Bounds()
				if from.Location().String() != "UTC" || !to.After(from) {
					t.Fatal("not UTC bounds")
				}
			}
		})
	}
}
func TestExactNumbers(t *testing.T) {
	for _, value := range []Money{"0", "-1", "9007199254740993", Money(strings.Repeat("9", 38))} {
		n, err := Integer(value)
		if err != nil || n.String() != string(value) {
			t.Fatalf("lost money %s", value)
		}
	}
	for _, value := range []Money{"", "-0", "01", "+1", "1.0", "1e3"} {
		if _, err := Integer(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"-1", "9007199254740992", "18446744073709551616"} {
		if _, err := Count(value); err == nil {
			t.Fatalf("unsafe count %s", value)
		}
	}
	if n, err := Count("9007199254740991"); err != nil || n != MaxSafeCount {
		t.Fatal(n, err)
	}
}
