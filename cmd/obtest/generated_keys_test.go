package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const generatedMaxStandard = "max-standard"
const generatedMaxSummary = "max-summary"
const generatedKeyCase = "generated-keys"
const generatedWideID = uint64(9223372036854775813)
const generatedNegativeData = "negative-data"
const generatedCleanupError = "cleanup-error"

type generatedPeerStep struct {
	query   string
	id      uint64
	count   byte
	row     string
	read    bool
	missing bool
	fail    bool
	extra   bool
}

func TestGeneratedKeyWorkflows(t *testing.T) {
	var definition caseDefinition
	for _, d := range caseDefinitions() {
		if d.name == generatedKeyCase {
			definition = d
		}
	}
	if definition.run == nil || !definition.mysqlOnly || definition.oracleOnly {
		t.Fatal("MySQL generated-key acceptance scenario is missing or incorrectly gated")
	}
	for _, fault := range []string{"valid-keys", generatedMaxStandard, generatedMaxSummary, "standard-no-id", "standard-mismatch", "standard-count", "standard-update-id", "summary-no-id", "summary-mismatch", "summary-count", "summary-update-id", "alter-error", "wide-standard-no-id", "wide-standard-range", "wide-standard-count", "wide-standard-in-range", "wide-standard-update-id", "wide-summary-no-id", "wide-summary-mismatch", "wide-summary-range", "wide-summary-count", "wide-summary-update-id", "final-count", "standard-read-error", "summary-error", "no-standard-row", "no-summary-row", "zero-data", generatedNegativeData, generatedCleanupError, "summary-extra-result"} {
		t.Run(fault, func(t *testing.T) {
			cfg := oceanbase.Config{User: "generated_key_fixture", Mode: oceanbase.ModeMySQL, Session: oceanbase.SessionOptions{SkipVersionProbe: true}, DialContext: generatedKeyPeer(t, fault)}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			s := &scenario{db: db, cfg: cfg, prefix: fixturePrefix, mode: oceanbase.ModeMySQL, cleanupTimeout: time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := runScenario(ctx, s, definition.run)
			want, state := statusFail, fixtureDroppedState
			if fault == "valid-keys" || fault == generatedMaxStandard || fault == generatedMaxSummary {
				want = statusPass
			}
			if fault == generatedCleanupError {
				state = cleanupFailed
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != state || (len(result.CleanupErrors) != 0) != (fault == generatedCleanupError) {
				t.Fatalf("generated-key workflow: %+v; want %s with %s", result, want, state)
			}
		})
	}
}

func generatedKeyPeer(t *testing.T, fault string) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	var connections atomic.Int32
	return func(context.Context, string, string) (net.Conn, error) {
		attempt := connections.Add(1)
		if attempt > 2 || attempt > 1 && fault != generatedNegativeData {
			t.Error("unexpected generated-key reconnect")
			return nil, fmt.Errorf("unexpected reconnect")
		}
		client, server := net.Pipe()
		if err := server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			serveGeneratedKeyPeer(t, server, fault, attempt > 1)
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		return client, nil
	}
}

func serveGeneratedKeyPeer(t *testing.T, c net.Conn, fault string, cleanupOnly bool) {
	t.Helper()
	greeting := append([]byte{10}, []byte("5.7.25-OceanBase\x00")...)
	greeting = append(greeting, 1, 0, 0, 0)
	greeting = append(greeting, []byte("12345678\x00")...)
	greeting = append(greeting, 3, 162, 45, 2, 0, 8, 0, 21)
	greeting = append(greeting, make([]byte, 10)...)
	greeting = append(greeting, []byte("123456789012\x00mysql_native_password\x00")...)
	if !writeCountPacket(t, c, 0, greeting) {
		return
	}
	if _, ok := readCountPacket(t, c, 1); !ok || !writeCountPacket(t, c, 2, []byte{0, 0, 0, 2, 0, 0, 0}) {
		return
	}
	steps := generatedKeyScript(fault)
	if cleanupOnly {
		steps = append(steps[:3:3], generatedPeerStep{query: fixtureDropTableSQL})
	}
	for i, step := range steps {
		command, ok := readCountPacket(t, c, 0)
		if !ok {
			return
		}
		if string(command) != "\x03"+step.query {
			t.Errorf("step %d: command=%q want SQL %q", i, command, step.query)
			return
		}
		if !serveGeneratedKeyStep(t, c, step) {
			return
		}
	}
	command, ok := readCountPacket(t, c, 0)
	if ok && (len(command) != 1 || command[0] != 1) {
		t.Errorf("expected COM_QUIT, got %q", command)
	}
}

func serveGeneratedKeyStep(t *testing.T, c net.Conn, step generatedPeerStep) bool {
	t.Helper()
	if step.fail {
		return writeCountPacket(t, c, 1, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'b', 'a', 'd'})
	}
	if step.read {
		column := []byte{3, 'd', 'e', 'f', 0, 0, 0, 2, 'I', 'D', 0, 12, 63, 0, 20, 0, 0, 0, 8, 32, 0, 0, 0, 0}
		packets := [][]byte{{1}, column, {254, 0, 0, 2, 0}}
		if !step.missing {
			packets = append(packets, append([]byte{byte(len(step.row))}, step.row...))
		}
		if step.row != "-1" {
			packets = append(packets, []byte{254, 0, 0, 2, 0})
		}
		for i, payload := range packets {
			if !writeCountPacket(t, c, byte(i+1), payload) {
				return false
			}
		}
		if step.row == "-1" {
			// Malformed unsigned data must discard the connection before cleanup.
			var next [1]byte
			if n, err := c.Read(next[:]); n != 0 || err != io.EOF {
				t.Errorf("malformed row did not close connection: n=%d err=%v", n, err)
			}
			return false
		}
		return true
	}
	payload := binary.LittleEndian.AppendUint64([]byte{0, step.count, 254}, step.id)
	status := byte(2)
	if step.extra {
		status |= 8
	}
	if !writeCountPacket(t, c, 1, append(payload, status, 0, 0, 0)) {
		return false
	}
	return !step.extra || writeCountPacket(t, c, 2, []byte{0, 0, 0, 2, 0, 0, 0})
}

func generatedKeyScript(fault string) []generatedPeerStep {
	steps := []generatedPeerStep{
		{query: "SET NAMES utf8mb4"},
		{query: "SET autocommit=1"},
		{query: "SET SESSION time_zone = '+00:00'"},
		{query: "CREATE TABLE OBT_OFFLINE_T (ID BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,V VARCHAR(100) UNIQUE) ENGINE=InnoDB"},
		{query: "INSERT INTO OBT_OFFLINE_T (V) VALUES ('exec')", count: 1, id: 7},
		{query: "SELECT ID FROM OBT_OFFLINE_T WHERE V='exec'", read: true, row: "7"},
		{query: "UPDATE OBT_OFFLINE_T SET V='after_exec' WHERE V='exec'", count: 1},
		{query: "INSERT INTO OBT_OFFLINE_T (V) VALUES ('execution')", count: 1, id: 13},
		{query: "SELECT ID FROM OBT_OFFLINE_T WHERE V='execution'", read: true, row: "13"},
		{query: "UPDATE OBT_OFFLINE_T SET V='after_execution' WHERE V='execution'", count: 1},
		{query: "ALTER TABLE OBT_OFFLINE_T AUTO_INCREMENT=9223372036854775808"},
		{query: "INSERT INTO OBT_OFFLINE_T (V) VALUES ('wide_exec')", count: 1, id: generatedWideID},
		{query: "SELECT ID FROM OBT_OFFLINE_T WHERE V='wide_exec'", read: true, row: "9223372036854775813"},
		{query: "UPDATE OBT_OFFLINE_T SET V='after_wide_exec' WHERE V='wide_exec'", count: 1},
		{query: "INSERT INTO OBT_OFFLINE_T (V) VALUES ('wide_execution')", count: 1, id: 9223372036854775837},
		{query: "SELECT ID FROM OBT_OFFLINE_T WHERE V='wide_execution'", read: true, row: "9223372036854775837"},
		{query: "UPDATE OBT_OFFLINE_T SET V='after_wide_execution' WHERE V='wide_execution'", count: 1},
		{query: fixtureCountSQL, read: true, row: "4"},
		{query: fixtureDropTableSQL},
	}
	last := len(steps) - 1
	switch fault {
	case generatedMaxStandard:
		steps[4].id, steps[5].row = 9223372036854775807, "9223372036854775807"
	case generatedMaxSummary:
		steps[14].id, steps[15].row = ^uint64(0), "18446744073709551615"
	case "standard-no-id":
		steps[4].id, last = 0, 5
	case "standard-mismatch":
		steps[5].row, last = "8", 5
	case "standard-count":
		steps[4].count, last = 0, 4
	case "standard-update-id":
		steps[6].id, last = 7, 6
	case "summary-no-id":
		steps[7].id, last = 0, 8
	case "summary-mismatch":
		steps[8].row, last = "14", 8
	case "summary-count":
		steps[7].count, last = 2, 7
	case "summary-update-id":
		steps[9].id, last = 13, 9
	case "alter-error":
		steps[10].fail, last = true, 10
	case "final-count":
		steps[17].row, last = "3", 17
	case "standard-read-error":
		steps[5].fail, last = true, 5
	case "summary-error":
		steps[7].fail, last = true, 7
	case "no-standard-row":
		steps[5].missing, last = true, 5
	case "no-summary-row":
		steps[8].missing, last = true, 8
	case "zero-data":
		steps[5].row, last = "0", 5
	case generatedNegativeData:
		steps[5].row, last = "-1", 5
	case generatedCleanupError:
		steps[18].fail = true
	case "summary-extra-result":
		steps[7].extra, last = true, 7
	default:
		last = applyWideGeneratedFault(steps, fault)
	}
	if last < len(steps)-1 {
		steps = append(steps[:last+1:last+1], steps[len(steps)-1])
	}
	return steps
}

func applyWideGeneratedFault(steps []generatedPeerStep, fault string) int {
	last := len(steps) - 1
	switch fault {
	case "wide-standard-no-id":
		steps[11].id, last = 0, 12
	case "wide-standard-range":
		steps[11].id, steps[12].row, last = 11, "11", 12
	case "wide-standard-count":
		steps[11].count, last = 0, 11
	case "wide-standard-in-range":
		steps[11].id, last = 7, 12
	case "wide-standard-update-id":
		steps[13].id, last = generatedWideID, 13
	case "wide-summary-no-id":
		steps[14].id, last = 0, 15
	case "wide-summary-mismatch":
		steps[15].row, last = "9223372036854775838", 15
	case "wide-summary-range":
		steps[14].id, steps[15].row, last = 5, "5", 15
	case "wide-summary-count":
		steps[14].count, last = 2, 14
	case "wide-summary-update-id":
		steps[16].id, last = generatedWideID, 16
	}
	return last
}
