package config

import (
	"context"
	"time"
)

// DiagnosticOptions bounds synchronous diagnostic delivery after releasing the driver gate.
// SQL and packets are represented by fingerprints and metadata, never by raw payloads.
type DiagnosticOptions struct {
	SlowThreshold           time.Duration
	MaxRequests, MaxPackets int
	Request                 func(context.Context, RequestDiagnostic)
	Packet                  func(context.Context, PacketDiagnostic)
}

// RequestDiagnostic describes one physical protocol command, including consumer time for Rows.
type RequestDiagnostic struct {
	Operation                        string
	Mode                             Mode
	ConnectionID, RequestID          uint32
	Duration                         time.Duration
	ReadBytes, WriteBytes            int64
	SQLFingerprint                   string
	SQLBytes                         int
	ErrorCode                        uint16
	SQLState                         string
	Failed                           bool
	DroppedPackets, DroppedRequests  int
	TraceID, SpanID                  [16]byte
	QueryStartMicros, QueryEndMicros int64
}

// PacketDiagnostic describes a logical message without exposing its payload.
type PacketDiagnostic struct {
	RequestID uint32
	Command   byte
	Write     bool
	Bytes     int64
	Sequence  byte
	Failed    bool
}

func (o DiagnosticOptions) validate() error {
	if o.SlowThreshold < 0 || o.MaxRequests < 0 || o.MaxRequests > 1024 || o.MaxPackets < 0 || o.MaxPackets > 1024 {
		return &ConfigError{Field: "Diagnostics"}
	}
	return nil
}
