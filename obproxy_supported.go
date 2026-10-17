//go:build (obproxy && linux && !android && (amd64 || arm64)) || (obproxytest && windows && amd64)

package oceanbase

const obProxyEnabled = true
