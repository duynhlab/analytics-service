//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/duynhlab/analytics-service/internal/core/domain"
)

// Only tests rewrite the fixed analytics namespace. Every run owns a fresh
// database and user; the service itself cannot select a database or perform DDL.
type fixtureConn struct {
	driver.Conn
	database string
}

func (c fixtureConn) Query(ctx context.Context, query string, args ...any) (driver.Rows, error) {
	return c.Conn.Query(ctx, strings.ReplaceAll(query, "analytics.", c.database+"."), args...)
}

type fixtureTable struct {
	name, columns string
	names         []string
}

var fixtureTables = []fixtureTable{
	{"order_orders", "id Int32, status String", []string{"id", "status"}},
	{"order_order_items", "id Int32, order_id Int32, product_id Int32, product_name String, quantity Int32, subtotal Int64", []string{"id", "order_id", "product_id", "product_name", "quantity", "subtotal"}},
	{"checkout_checkout_sessions", "id String, order_id Nullable(String), currency String, created_at DateTime64(6, 'UTC')", []string{"id", "order_id", "currency", "created_at"}},
	{"payment_payments", "id Int64, order_id Nullable(Int64), currency String", []string{"id", "order_id", "currency"}},
	{"payment_ledger_transactions", "id Int64, payment_id Int64, kind String, created_at DateTime64(6, 'UTC')", []string{"id", "payment_id", "kind", "created_at"}},
	{"payment_ledger_entries", "id Int64, transaction_id Int64, account_id Int64, direction String, amount_minor Int64", []string{"id", "transaction_id", "account_id", "direction", "amount_minor"}},
	{"payment_ledger_accounts", "id Int64, name String", []string{"id", "name"}},
}

func TestServingIntegration(t *testing.T) {
	addr := os.Getenv("ANALYTICS_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		t.Fatal("explicit integration gate requires ANALYTICS_TEST_CLICKHOUSE_ADDR; use the disposable Linux test server documented in README")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: os.Getenv("ANALYTICS_TEST_CLICKHOUSE_USER"), Password: os.Getenv("ANALYTICS_TEST_CLICKHOUSE_PASSWORD")}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	database := fmt.Sprintf("analytics_test_%d", time.Now().UnixNano())
	user := database + "_read"
	exec := func(sql string) {
		t.Helper()
		if err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("fixture statement failed: %v", err)
		}
	}
	exec("CREATE DATABASE " + database)
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if err := admin.Exec(cleanup, "DROP DATABASE "+database); err != nil {
			t.Error(err)
		}
		if err := admin.Exec(cleanup, "DROP USER IF EXISTS "+user); err != nil {
			t.Error(err)
		}
	})
	// Credentials exist only inside this disposable fixture and are never service defaults.
	exec("CREATE USER " + user + " IDENTIFIED WITH sha256_password BY 'fixture-only-password'")
	tables := append([]fixtureTable(nil), fixtureTables...)
	for _, source := range []string{"order", "checkout", "payment"} {
		tables = append(tables, fixtureTable{source + "_analytics_cdc_heartbeat", "id Int32, emitted_at DateTime64(6, 'UTC')", []string{"id", "emitted_at"}})
	}
	for _, table := range tables {
		raw := database + "." + table.name
		exec("CREATE TABLE " + raw + " (" + table.columns + ", _peerdb_version UInt64, _peerdb_is_deleted UInt8) ENGINE=MergeTree ORDER BY id")
		fields := make([]string, len(table.names))
		for i, name := range table.names {
			fields[i] = fmt.Sprintf("latest.%d AS %s", i+1, name)
		}
		exec("CREATE VIEW " + raw + "_live DEFINER=CURRENT_USER SQL SECURITY DEFINER AS SELECT " + strings.Join(fields, ",") + " FROM (SELECT id, argMax(tuple(" + strings.Join(table.names, ",") + ",_peerdb_is_deleted),_peerdb_version) AS latest FROM " + raw + " GROUP BY id) WHERE latest." + fmt.Sprint(len(table.names)+1) + " = 0")
		exec("GRANT SELECT ON " + raw + "_live TO " + user)
	}
	insert := func(table, values string) {
		t.Helper()
		exec("INSERT INTO " + database + "." + table + " VALUES " + values)
	}
	for _, source := range []string{"order", "checkout", "payment"} {
		insert(source+"_analytics_cdc_heartbeat", "(1,now64(6),1,0)")
	}
	insert("order_orders", "(1,'completed',1,0),(2,'completed',1,0),(3,'processing',1,0),(4,'completed',1,0)")
	insert("order_order_items", "(1,1,101,'Historical name',2,9007199254740993,1,0),(1,1,101,'Historical name',2,9007199254740993,2,0),(2,1,102,'Deleted item',1,999,1,0),(2,1,102,'Deleted item',1,999,2,1)")
	insert("checkout_checkout_sessions", "('c1','1','USD','2026-10-01 00:00:00',1,0),('c2','2','USD','2026-10-01 12:00:00',1,0),('c3','3','USD','2026-10-01 12:00:00',1,0),('c3',NULL,'USD','2026-10-01 12:00:00',2,0),('gone','4','USD','2026-10-01 12:00:00',1,0),('gone','4','USD','2026-10-01 12:00:00',2,1),('end',NULL,'USD','2026-10-03 00:00:00',1,0)")
	insert("payment_payments", "(1,1,'USD',1,0),(2,2,'USD',1,0),(3,NULL,'USD',1,0),(4,3,'EUR',1,0),(5,4,'USD',1,0)")
	insert("payment_ledger_accounts", "(6,'merchant_revenue',1,0),(7,'cash',1,0)")
	// September capture refunded in October; October cohort capture after its end;
	// October-only reversal is negative settled; orphan payment money remains included.
	insert("payment_ledger_transactions", "(8,1,'capture','2026-10-01 00:00:00',1,0),(9,2,'capture','2026-09-30 23:59:59.999998',1,0),(10,2,'reversal','2026-09-30 23:59:59.999999',1,0),(11,1,'capture','2026-10-01 12:00:00',1,0),(12,1,'capture','2026-10-01 12:00:00',1,0),(13,2,'refund','2026-10-02 00:00:00',1,0),(14,3,'capture','2026-10-02 12:00:00',1,0),(4,4,'capture','2026-10-01 12:00:00',1,0),(15,2,'capture','2026-10-03 00:00:00',1,0),(16,5,'reversal','2026-10-02 00:00:00',1,0)")
	insert("payment_ledger_entries", "(17,8,6,'credit',9007199254740993,1,0),(17,8,6,'credit',9007199254740993,2,0),(7,8,7,'debit',9007199254740993,1,0),(18,9,6,'credit',1000,1,0),(10,10,6,'debit',1000,1,0),(11,11,6,'credit',9000000000000000000,1,0),(12,12,6,'credit',9000000000000000000,1,0),(19,13,6,'debit',200,1,0),(20,14,6,'credit',50,1,0),(21,4,6,'credit',99999,1,0),(22,15,6,'credit',500,1,0),(23,16,6,'debit',300,1,0)")
	reader, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Database: database, Username: user, Password: "fixture-only-password"}, DialTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	repo, err := New(fixtureConn{Conn: reader, database: database})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	hb, err := repo.Heartbeats(ctx)
	if err != nil || len(hb) != 3 || hb[0].At == nil {
		t.Fatalf("heartbeats=%+v err=%v", hb, err)
	}
	filters := domain.Filters{Currency: "USD", From: "2026-10-01", To: "2026-10-03"}
	facts, err := repo.Read(ctx, filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Daily) != 2 || facts.Daily[0].Settled != "18009007199254740993" || facts.Daily[1].Settled != "-250" || facts.Daily[1].Refunded != "200" {
		t.Fatalf("event-date/dedup/money failure: %+v", facts.Daily)
	}
	if facts.CapturedOrders != 1 || facts.Funnel != (domain.Funnel{Started: 3, Created: 2, Captured: 2, Completed: 2}) {
		t.Fatalf("cohort/NULL failure: %+v", facts)
	}
	if len(facts.Products) != 1 || facts.Products[0].Subtotal != "9007199254740993" || facts.Products[0].Quantity != 2 || facts.Products[0].Orders != 1 {
		t.Fatalf("item join/tombstone failure: %+v", facts.Products)
	}
	for _, sql := range []string{"SELECT * FROM " + database + ".payment_payments", "INSERT INTO " + database + ".order_orders VALUES (99,'completed',1,0)", "CREATE TABLE " + database + ".forbidden (id Int32) ENGINE=Memory"} {
		err := reader.Exec(ctx, sql)
		var exception *clickhouse.Exception
		if !errors.As(err, &exception) || exception.Code != 497 {
			t.Fatalf("expected ACCESS_DENIED for %s, got %v", sql, err)
		}
	}
	canceled, c := context.WithCancel(ctx)
	c()
	if _, err := repo.Read(canceled, filters); err == nil {
		t.Fatal("canceled read succeeded")
	}
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	start := time.Now()
	err = repo.query(short, "cancel-test", "SELECT sleep(1) FROM analytics.order_analytics_cdc_heartbeat_live", nil, func(rows driver.Rows) error {
		for rows.Next() {
		}
		return nil
	})
	stop()
	if err == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatal("in-flight cancellation did not bound query", err)
	}
	// Invalid ledger directions fail closed rather than fabricate a zero contribution.
	insert("payment_ledger_entries", "(24,8,6,'debit',1,1,0)")
	if _, err := repo.Read(ctx, filters); err == nil {
		t.Fatal("accepted invalid merchant leg")
	}
}
