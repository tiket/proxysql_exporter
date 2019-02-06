// Copyright 2016-2017 Percona LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/smartystreets/goconvey/convey"
	"gopkg.in/DATA-DOG/go-sqlmock.v1"
)

var nameRE = regexp.MustCompile(`fqName: "(\w+)"`)

// https://github.com/prometheus/client_golang/issues/322
func getName(d *prometheus.Desc) string {
	m := nameRE.FindStringSubmatch(d.String())
	if len(m) != 2 {
		panic("failed to get metric name from " + d.String())
	}
	return m[1]
}

type metricResult struct {
	name       string
	labels     prometheus.Labels
	value      float64
	metricType dto.MetricType
}

// https://github.com/prometheus/client_golang/issues/323
func readMetric(m prometheus.Metric) *metricResult {
	pb := &dto.Metric{}
	err := m.Write(pb)
	if err != nil {
		panic(err)
	}

	name := getName(m.Desc())
	labels := make(prometheus.Labels, len(pb.Label))
	for _, v := range pb.Label {
		labels[v.GetName()] = v.GetValue()
	}
	if pb.Gauge != nil {
		return &metricResult{name, labels, pb.GetGauge().GetValue(), dto.MetricType_GAUGE}
	}
	if pb.Counter != nil {
		return &metricResult{name, labels, pb.GetCounter().GetValue(), dto.MetricType_COUNTER}
	}
	if pb.Untyped != nil {
		return &metricResult{name, labels, pb.GetUntyped().GetValue(), dto.MetricType_UNTYPED}
	}
	panic("Unsupported metric type")
}

func sanitizeQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	q = strings.Replace(q, "(", "\\(", -1)
	q = strings.Replace(q, ")", "\\)", -1)
	q = strings.Replace(q, "*", "\\*", -1)
	return q
}

func TestScrapeMySQLGlobal(t *testing.T) {
	convey.Convey("Metrics are lowercase", t, convey.FailureContinues, func(cv convey.C) {
		for c, m := range mySQLGlobalMetrics {
			cv.So(c, convey.ShouldEqual, strings.ToLower(c))
			cv.So(m.name, convey.ShouldEqual, strings.ToLower(m.name))
		}
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error opening a stub database connection: %s", err)
	}
	defer db.Close()

	columns := []string{"Variable_Name", "Variable_Value"}
	rows := sqlmock.NewRows(columns).
		AddRow("Active_Transactions", "3").
		AddRow("Backend_query_time_nsec", "76355784684851").
		AddRow("Client_Connections_aborted", "0").
		AddRow("Client_Connections_connected", "64").
		AddRow("Client_Connections_created", "1087931").
		AddRow("Servers_table_version", "2019470")
	mock.ExpectQuery(mySQLGlobalQuery).WillReturnRows(rows)

	ch := make(chan prometheus.Metric)
	go func() {
		if err = scrapeMySQLGlobal(db, ch); err != nil {
			t.Errorf("error calling function on test: %s", err)
		}
		close(ch)
	}()

	counterExpected := []metricResult{
		{"proxysql_mysql_status_active_transactions", prometheus.Labels{}, 3, dto.MetricType_GAUGE},
		{"proxysql_mysql_status_backend_query_time_nsec", prometheus.Labels{}, 76355784684851, dto.MetricType_UNTYPED},
		{"proxysql_mysql_status_client_connections_aborted", prometheus.Labels{}, 0, dto.MetricType_COUNTER},
		{"proxysql_mysql_status_client_connections_connected", prometheus.Labels{}, 64, dto.MetricType_GAUGE},
		{"proxysql_mysql_status_client_connections_created", prometheus.Labels{}, 1087931, dto.MetricType_COUNTER},
		{"proxysql_mysql_status_servers_table_version", prometheus.Labels{}, 2019470, dto.MetricType_UNTYPED},
	}
	convey.Convey("Metrics comparison", t, convey.FailureContinues, func(cv convey.C) {
		for _, expect := range counterExpected {
			got := *readMetric(<-ch)
			cv.So(got, convey.ShouldResemble, expect)
		}
	})

	// Ensure all SQL queries were executed
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}

func TestScrapeMySQLGlobalError(t *testing.T) {
	db1, mock1, err1 := sqlmock.New()
	if err1 != nil {
		t.Fatalf("error opening a stub database connection: %s", err1)
	}
	defer db1.Close()

	mock1.ExpectQuery(mySQLGlobalQuery).WillReturnError(errors.New("an error"))
	ch1 := make(chan prometheus.Metric)

	go func() {
		scrapeMySQLGlobal(db1, ch1)
		close(ch1)
	}()

	db2, mock2, err2 := sqlmock.New()
	if err2 != nil {
		t.Fatalf("error opening a stub database connection: %s", err2)
	}
	defer db2.Close()

	columns := []string{"Variable_Name", "Variable_Value"}
	rows := sqlmock.NewRows(columns).AddRow("Active_Transactions", "3")
	mock2.ExpectQuery(sanitizeQuery(mySQLGlobalQuery)).WillReturnRows(rows)

	ch2 := make(chan prometheus.Metric)
	go func() {
		scrapeMySQLGlobal(db2, ch2)
		close(ch2)
	}()

	_ = *readMetric(<-ch2)
}

func TestScrapeMySQLConnectionPool(t *testing.T) {
	convey.Convey("Metrics are lowercase", t, convey.FailureContinues, func(cv convey.C) {
		for c, m := range mySQLconnectionPoolMetrics {
			cv.So(c, convey.ShouldEqual, strings.ToLower(c))
			cv.So(m.name, convey.ShouldEqual, strings.ToLower(m.name))
		}
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error opening a stub database connection: %s", err)
	}
	defer db.Close()

	columns := []string{"hostgroup", "srv_host", "srv_port", "status", "ConnUsed", "ConnFree", "ConnOK", "ConnERR",
		"Queries", "Bytes_data_sent", "Bytes_data_recv", "Latency_us"}
	rows := sqlmock.NewRows(columns).
		AddRow("0", "10.91.142.80", "3306", "ONLINE", "0", "45", "1895677", "46", "197941647", "10984550806", "321063484988", "163").
		AddRow("0", "10.91.142.82", "3306", "SHUNNED", "0", "97", "39859", "0", "386686994", "21643682247", "641406745151", "255").
		AddRow("1", "10.91.142.88", "3306", "OFFLINE_SOFT", "0", "18", "31471", "6391", "255993467", "14327840185", "420795691329", "283").
		AddRow("2", "10.91.142.89", "3306", "OFFLINE_HARD", "0", "18", "31471", "6391", "255993467", "14327840185", "420795691329", "283")
	mock.ExpectQuery(sanitizeQuery(mySQLconnectionPoolQuery)).WillReturnRows(rows)

	ch := make(chan prometheus.Metric)
	go func() {
		if err = scrapeMySQLConnectionPool(db, ch); err != nil {
			t.Errorf("error calling function on test: %s", err)
		}
		close(ch)
	}()

	counterExpected := []metricResult{
		{"proxysql_connection_pool_status", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 1, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 45, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 1895677, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 46, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 197941647, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 10984550806, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 321063484988, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.80:3306"}, 163, dto.MetricType_GAUGE},

		{"proxysql_connection_pool_status", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 2, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 97, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 39859, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 0, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 386686994, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 21643682247, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 641406745151, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", prometheus.Labels{"hostgroup": "0", "endpoint": "10.91.142.82:3306"}, 255, dto.MetricType_GAUGE},

		{"proxysql_connection_pool_status", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 3, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 18, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 31471, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 6391, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 255993467, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 14327840185, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 420795691329, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", prometheus.Labels{"hostgroup": "1", "endpoint": "10.91.142.88:3306"}, 283, dto.MetricType_GAUGE},

		{"proxysql_connection_pool_status", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 4, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_used", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 0, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_free", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 18, dto.MetricType_GAUGE},
		{"proxysql_connection_pool_conn_ok", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 31471, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_conn_err", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 6391, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_queries", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 255993467, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_sent", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 14327840185, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_bytes_data_recv", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 420795691329, dto.MetricType_COUNTER},
		{"proxysql_connection_pool_latency_us", prometheus.Labels{"hostgroup": "2", "endpoint": "10.91.142.89:3306"}, 283, dto.MetricType_GAUGE},
	}
	convey.Convey("Metrics comparison", t, convey.FailureContinues, func(cv convey.C) {
		for _, expect := range counterExpected {
			got := *readMetric(<-ch)
			cv.So(got, convey.ShouldResemble, expect)
		}
	})

	// Ensure all SQL queries were executed
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}

func TestScrapeMySQLConnectionPoolError(t *testing.T) {
	db1, mock1, err1 := sqlmock.New()
	if err1 != nil {
		t.Fatalf("error opening a stub database connection: %s", err1)
	}
	defer db1.Close()

	mock1.ExpectQuery(mySQLconnectionPoolQuery).WillReturnError(errors.New("an error"))
	ch1 := make(chan prometheus.Metric)

	go func() {
		scrapeMySQLConnectionPool(db1, ch1)
		close(ch1)
	}()

	mySQLconnectionPoolMetrics = map[string]*metric{
		"hostgroup": {},
		"latency_us": {"latency_us", prometheus.GaugeValue,
			"The currently ping time in microseconds, as reported from Monitor."},
		"latency_ms": {"latency_us", prometheus.GaugeValue,
			"The currently ping time in microseconds, as reported from Monitor."},
	}

	db2, mock2, err2 := sqlmock.New()
	if err2 != nil {
		t.Fatalf("error opening a stub database connection: %s", err2)
	}
	defer db2.Close()

	columns := []string{"hostgroup", "srv_host", "srv_port", "status", "ConnUsed", "ConnFree", "ConnOK", "ConnERR",
		"Queries", "Bytes_data_sent", "Bytes_data_recv", "Latency_us"}
	rows := sqlmock.NewRows(columns).AddRow("0", "10.91.142.80", "3306", "ONLINE", "0", "45", "1895677", "46", "197941647", "10984550806", "321063484988", "163")
	mock2.ExpectQuery(sanitizeQuery(mySQLconnectionPoolQuery)).WillReturnRows(rows)

	ch2 := make(chan prometheus.Metric)
	go func() {
		scrapeMySQLConnectionPool(db2, ch2)
		close(ch2)
	}()

	_ = *readMetric(<-ch2)
}

func TestScrapeMySQLConnectionList(t *testing.T) {
	convey.Convey("Metrics are lowercase", t, convey.FailureContinues, func(cv convey.C) {
		for c, m := range mySQLconnectionListMetrics {
			cv.So(c, convey.ShouldEqual, strings.ToLower(c))
			cv.So(m.name, convey.ShouldEqual, strings.ToLower(m.name))
		}
	})

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error opening a stub database connection: %s", err)
	}
	defer db.Close()

	columns := []string{"cli_host", "srv_host"}
	rows := sqlmock.NewRows(columns).
		AddRow("10.91.142.80", "10.91.142.90").
		AddRow("10.91.142.82", "10.91.142.92").
		AddRow("10.91.142.88", "10.91.142.98").
		AddRow("10.91.142.89", "10.91.142.99")
	mock.ExpectQuery(sanitizeQuery(mySQLConnectionListQuery)).WillReturnRows(rows)

	ch := make(chan prometheus.Metric)
	go func() {
		if err = scrapeMySQLConnectionList(db, ch); err != nil {
			t.Errorf("error calling function on test: %s", err)
		}
		close(ch)
	}()

	counterExpected := []metricResult{
		{"proxysql_processlist_client_connection_list", prometheus.Labels{"client_host": "10.91.142.80"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_client_connection_list", prometheus.Labels{"client_host": "10.91.142.82"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_client_connection_list", prometheus.Labels{"client_host": "10.91.142.88"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_client_connection_list", prometheus.Labels{"client_host": "10.91.142.89"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_server_connection_list", prometheus.Labels{"server_host": "10.91.142.90"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_server_connection_list", prometheus.Labels{"server_host": "10.91.142.92"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_server_connection_list", prometheus.Labels{"server_host": "10.91.142.98"}, 1, dto.MetricType_GAUGE},
		{"proxysql_processlist_server_connection_list", prometheus.Labels{"server_host": "10.91.142.99"}, 1, dto.MetricType_GAUGE},
	}

	// The returned metrics has indeterminate order, so we need to check metrics with same name and label value.
	convey.Convey("Metrics comparison", t, convey.FailureContinues, func(cv convey.C) {
		for i := 0; i < len(counterExpected); i++ {
			got := *readMetric(<-ch)
			for _, expect := range counterExpected {
				if got.name == "proxysql_processlist_server_connection_list" {
					if got.labels["server_host"] == expect.labels["server_host"] {
						cv.So(got, convey.ShouldResemble, expect)
						continue
					}
				} else {
					if got.labels["client_host"] == expect.labels["client_host"] {
						cv.So(got, convey.ShouldResemble, expect)
						continue
					}
				}
			}
		}
	})

	// Ensure all SQL queries were executed
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}

func TestScrapeMySQLConnectionListError(t *testing.T) {
	db1, mock1, err1 := sqlmock.New()
	if err1 != nil {
		t.Fatalf("error opening a stub database connection: %s", err1)
	}
	defer db1.Close()

	mock1.ExpectQuery(mySQLConnectionListQuery).WillReturnError(errors.New("an error"))
	ch1 := make(chan prometheus.Metric)

	go func() {
		scrapeMySQLConnectionList(db1, ch1)
		close(ch1)
	}()

	mySQLconnectionListMetrics = map[string]*metric{
		"client_connection_list": {},
		"server_connection_list": {},
	}

	db2, mock2, err2 := sqlmock.New()
	if err2 != nil {
		t.Fatalf("error opening a stub database connection: %s", err2)
	}
	defer db2.Close()

	columns := []string{"cli_host", "srv_host"}
	rows := sqlmock.NewRows(columns).AddRow("10.91.142.80", "10.91.142.90")
	mock2.ExpectQuery(sanitizeQuery(mySQLConnectionListQuery)).WillReturnRows(rows)

	ch2 := make(chan prometheus.Metric)
	go func() {
		scrapeMySQLConnectionList(db2, ch2)
		close(ch2)
	}()

	_ = *readMetric(<-ch2)
}

func TestExporter(t *testing.T) {
	if testing.Short() {
		t.Skip("-short is passed, skipping integration test")
	}

	// wait up to 30 seconds for ProxySQL to become available
	exporter := NewExporter("admin:admin@tcp(127.0.0.1:16032)/", true, true, true)
	for i := 0; i < 30; i++ {
		db, err := exporter.db()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		// configure ProxySQL
		for _, q := range strings.Split(`
DELETE FROM mysql_servers;
INSERT INTO mysql_servers(hostgroup_id, hostname, port) VALUES (1, 'mysql', 3306);
INSERT INTO mysql_servers(hostgroup_id, hostname, port) VALUES (1, 'percona-server', 3306);
LOAD MYSQL SERVERS TO RUNTIME;
SAVE MYSQL SERVERS TO DISK;

DELETE FROM mysql_users;
INSERT INTO mysql_users(username, password, default_hostgroup) VALUES ('root', '', 1);
INSERT INTO mysql_users(username, password, default_hostgroup) VALUES ('monitor', 'monitor', 1);
LOAD MYSQL USERS TO RUNTIME;
SAVE MYSQL USERS TO DISK;
`, ";") {
			q = strings.TrimSpace(q)
			if q == "" {
				continue
			}
			_, err = db.Exec(q)
			if err != nil {
				t.Fatalf("Failed to execute %q\n%s", q, err)
			}
		}
		break
	}

	convey.Convey("Metrics descriptions", t, convey.FailureContinues, func(cv convey.C) {
		ch := make(chan *prometheus.Desc)
		go func() {
			exporter.Describe(ch)
			close(ch)
		}()

		descs := make(map[string]struct{})
		for d := range ch {
			descs[d.String()] = struct{}{}
		}

		cv.So(descs, convey.ShouldContainKey,
			`Desc{fqName: "proxysql_connection_pool_latency_us", help: "The currently ping time in microseconds, as reported from Monitor.", constLabels: {}, variableLabels: [hostgroup endpoint]}`)
	})

	convey.Convey("Metrics data", t, convey.FailureContinues, func(cv convey.C) {
		ch := make(chan prometheus.Metric)
		go func() {
			exporter.Collect(ch)
			close(ch)
		}()

		var metrics []metricResult
		for m := range ch {
			got := *readMetric(m)
			got.value = 0 // ignore actual values in comparison for now
			metrics = append(metrics, got)
		}

		for _, m := range metrics {
			cv.So(m.name, convey.ShouldEqual, strings.ToLower(m.name))
			for k := range m.labels {
				cv.So(k, convey.ShouldEqual, strings.ToLower(k))
			}
		}

		cv.So(metricResult{"proxysql_connection_pool_latency_us", prometheus.Labels{"hostgroup": "1", "endpoint": "mysql:3306"}, 0, dto.MetricType_GAUGE},
			convey.ShouldBeIn, metrics)
		cv.So(metricResult{"proxysql_connection_pool_latency_us", prometheus.Labels{"hostgroup": "1", "endpoint": "percona-server:3306"}, 0, dto.MetricType_GAUGE},
			convey.ShouldBeIn, metrics)
	})
}
