package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const countFixtureTable = "OBT_OFFLINE_T"
const countExecutionOverflow = "execution-overflow"

type countPeerStep struct {
	query string
	count byte
}

func TestRowsAffectedWorkflows(t *testing.T) {
	for _, profile := range []struct {
		name   string
		mode   oceanbase.Mode
		policy oceanbase.RowsAffectedMode
	}{
		{"counts-matched", oceanbase.ModeMySQL, oceanbase.MatchedRows},
		{"counts-changed", oceanbase.ModeMySQL, oceanbase.ChangedRows},
		{"counts-matched", oceanbase.ModeOracle, oceanbase.MatchedRows},
	} {
		var definition caseDefinition
		for _, d := range caseDefinitions() {
			if d.name == profile.name {
				definition = d
			}
		}
		if definition.run == nil || definition.configure == nil {
			t.Fatalf("row-count acceptance scenario %s is missing", profile.name)
		}
		if definition.oracleOnly || definition.mysqlOnly != (profile.policy == oceanbase.ChangedRows) {
			t.Fatal("row-count mode gate is incorrect")
		}
		for _, fault := range []string{"none", "exec-noop", "execution-noop", "batch-noop", "insert", "change", "missing", "upsert-noop", "upsert-change", "upsert-insert", "delete", "empty-delete", "persisted", "overflow", countExecutionOverflow, "batch-overflow", "execution-error", "batch-error"} {
			if profile.mode == oceanbase.ModeOracle && (fault == "upsert-noop" || fault == "upsert-change" || fault == "upsert-insert") {
				continue
			}
			t.Run(fmt.Sprintf("%s/%v/%s", profile.name, profile.mode, fault), func(t *testing.T) {
				cfg := oceanbase.Config{User: "fixture", Mode: profile.mode, RowsAffectedMode: oceanbase.ChangedRows, Session: oceanbase.SessionOptions{SkipVersionProbe: true}}
				if profile.policy == oceanbase.ChangedRows {
					cfg.RowsAffectedMode = oceanbase.MatchedRows
				}
				definition.configure(&cfg)
				if cfg.RowsAffectedMode != profile.policy {
					t.Fatal("scenario did not override the target count policy")
				}
				cfg.DialContext = countPeer(t, profile.mode, profile.policy, fault)
				connector, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer func() { _ = db.Close() }()
				s := &scenario{db: db, cfg: cfg, prefix: "OBT_OFFLINE", mode: profile.mode, cleanupTimeout: time.Second}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result := runScenario(ctx, s, definition.run)
				want := statusFail
				if fault == "none" {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState {
					t.Fatalf("row-count workflow: %+v", result)
				}
			})
		}
	}
}

func countPeer(t *testing.T, mode oceanbase.Mode, policy oceanbase.RowsAffectedMode, fault string) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	var connections atomic.Int32
	return func(context.Context, string, string) (net.Conn, error) {
		attempt := connections.Add(1)
		if attempt > 2 || attempt > 1 && fault != countExecutionOverflow {
			t.Error("unexpected row-count reconnect")
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
			serveCountPeer(t, server, mode, policy, fault, attempt > 1)
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		return client, nil
	}
}

func serveCountPeer(t *testing.T, c net.Conn, mode oceanbase.Mode, policy oceanbase.RowsAffectedMode, fault string, cleanupOnly bool) {
	t.Helper()
	status := byte(2)
	if mode == oceanbase.ModeOracle {
		status |= 4
	}
	// Literal protocol-4.1 greeting: native auth, transactions and CLIENT_FOUND_ROWS.
	greeting := append([]byte{10}, []byte("5.7.25-OceanBase\x00")...)
	greeting = append(greeting, 1, 0, 0, 0)
	greeting = append(greeting, []byte("12345678\x00")...)
	greeting = append(greeting, 3, 162, 45, 2, 0, 8, 0, 21)
	greeting = append(greeting, make([]byte, 10)...)
	greeting = append(greeting, []byte("123456789012\x00mysql_native_password\x00")...)
	if !writeCountPacket(t, c, 0, greeting) {
		return
	}
	response, ok := readCountPacket(t, c, 1)
	if !ok || len(response) < 4 {
		return
	}
	if found := binary.LittleEndian.Uint32(response)&2 != 0; found != (policy == oceanbase.MatchedRows) {
		t.Error("CLIENT_FOUND_ROWS does not match the selected scenario")
		return
	}
	if !writeCountPacket(t, c, 2, []byte{0, 0, 0, status, 0, 0, 0}) {
		return
	}
	steps, faultIndex := countPeerScript(mode, policy, fault)
	if cleanupOnly {
		steps = append(steps[:3:3], countPeerStep{query: fixtureDropTableSQL})
		faultIndex = -1
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
		if step.query == "SELECT COUNT(*) FROM "+countFixtureTable {
			value := byte('0')
			if fault == "persisted" {
				value = '1'
			}
			column := []byte{3, 'd', 'e', 'f', 0, 0, 0, 1, 'N', 0, 12, 63, 0, 20, 0, 0, 0, 8, 0, 0, 0, 0, 0}
			for j, payload := range [][]byte{{1}, column, {254, 0, 0, status, 0}, {1, value}, {254, 0, 0, status, 0}} {
				if !writeCountPacket(t, c, byte(j+1), payload) {
					return
				}
			}
		} else {
			payload := []byte{0, step.count, 0, status, 0, 0, 0}
			if i == faultIndex && strings.HasSuffix(fault, "overflow") {
				payload = []byte{0, 254, 0, 0, 0, 0, 0, 0, 0, 128, 0, status, 0, 0, 0}
			}
			if i == faultIndex && strings.HasSuffix(fault, "-error") {
				payload = []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'b', 'a', 'd'}
			}
			if !writeCountPacket(t, c, 1, payload) {
				return
			}
			if i == faultIndex && fault == countExecutionOverflow {
				return
			}
		}
	}
	command, ok := readCountPacket(t, c, 0)
	if ok && (len(command) != 1 || command[0] != 1) {
		t.Errorf("expected COM_QUIT, got %q", command)
	}
}

func countPeerScript(mode oceanbase.Mode, policy oceanbase.RowsAffectedMode, fault string) ([]countPeerStep, int) {
	steps := []countPeerStep{{query: "SET NAMES utf8mb4"}, {query: "SET autocommit=1"}, {query: "SET SESSION time_zone = '+00:00'"}, {query: "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V INTEGER)"}}
	if mode == oceanbase.ModeMySQL {
		steps[3].query += " ENGINE=InnoDB"
	}
	noChange := byte(1)
	if policy == oceanbase.ChangedRows {
		noChange = 0
	}
	operations := []countPeerStep{
		{"INSERT INTO OBT_OFFLINE_T VALUES (1,7)", 1},
		{"UPDATE OBT_OFFLINE_T SET V=7 WHERE ID=1", noChange},
		{"UPDATE OBT_OFFLINE_T SET V=8 WHERE ID=1", 1},
		{"UPDATE OBT_OFFLINE_T SET V=8 WHERE ID=99", 0},
	}
	names := []string{"insert", "noop", "change", "missing"}
	deleted := byte(1)
	if mode == oceanbase.ModeMySQL {
		operations = append(operations, countPeerStep{"INSERT INTO OBT_OFFLINE_T VALUES (1,8) ON DUPLICATE KEY UPDATE V=8", noChange}, countPeerStep{"INSERT INTO OBT_OFFLINE_T VALUES (1,9) ON DUPLICATE KEY UPDATE V=9", 2}, countPeerStep{"INSERT INTO OBT_OFFLINE_T VALUES (2,9) ON DUPLICATE KEY UPDATE V=9", 1})
		names = append(names, "upsert-noop", "upsert-change", "upsert-insert")
		deleted = 2
	}
	operations = append(operations, countPeerStep{"DELETE FROM OBT_OFFLINE_T", deleted}, countPeerStep{"DELETE FROM OBT_OFFLINE_T", 0})
	names = append(names, "delete", "empty-delete")
	faultIndex := -1
	for _, path := range []string{"exec", countExecutionPath, "batch"} {
		for i, operation := range operations {
			steps = append(steps, operation)
			if fault == names[i] || fault == path+"-"+names[i] || fault == "overflow" || fault == path+"-overflow" || fault == path+"-error" {
				faultIndex = len(steps) - 1
				steps[faultIndex].count++
				steps = append(steps, countPeerStep{query: fixtureDropTableSQL})
				return steps, faultIndex
			}
		}
	}
	return append(steps, countPeerStep{query: "SELECT COUNT(*) FROM " + countFixtureTable}, countPeerStep{query: fixtureDropTableSQL}), faultIndex
}

func readCountPacket(t *testing.T, c net.Conn, sequence byte) ([]byte, bool) {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(c, header[:]); err != nil {
		t.Error(err)
		return nil, false
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if n > 4096 || header[3] != sequence {
		t.Errorf("invalid fixture packet length/sequence: %v", header)
		return nil, false
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c, payload); err != nil {
		t.Error(err)
		return nil, false
	}
	return payload, true
}

func writeCountPacket(t *testing.T, c net.Conn, sequence byte, payload []byte) bool {
	t.Helper()
	packet := append([]byte{byte(len(payload)), byte(len(payload) >> 8), 0, sequence}, payload...)
	if _, err := c.Write(packet); err != nil {
		t.Error(err)
		return false
	}
	return true
}
