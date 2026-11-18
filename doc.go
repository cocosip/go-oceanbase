// Package oceanbase implements an OceanBase database/sql driver for MySQL and
// Oracle compatibility modes. The minimum supported Go version is 1.26.
// The driver is a release candidate: direct-target acceptance is complete for
// OceanBase 4.4.2.2 in both compatibility modes, and the repository documents
// the deployment combinations that remain unverified. Use sql.OpenDB with
// NewConnector to supply a logger, TLS configuration or a custom dialer
// without global state.
package oceanbase
