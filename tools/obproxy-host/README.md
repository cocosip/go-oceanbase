# OBProxy native host

`main.c` is an optional standalone process used by the public `obproxy` package.
It loads the deployment's trusted OBProxy library and isolates native signals,
locale, allocator, threads and logging from the Go process. Business traffic uses
an ordinary Unix socket. Default Go builds do not require cgo or a C compiler.

On Linux, build the host separately from the repository root:

```sh
gcc -std=c11 -O2 -Wall -Wextra -Werror -o obproxy-host tools/obproxy-host/main.c -ldl -pthread
```

Pass trusted absolute host and library paths to `obproxy.NewOBProxyDialer`.
Production native support requires `-tags=obproxy` on Linux amd64 or arm64.
The root `oceanbase.NewOBProxyDialer` remains a compatibility entry point.

The host reads a length-prefixed configuration on stdin, reports `OBV1` readiness
on stdout, and exits when the parent's control pipe closes, including while native
initialization is blocked. Closing `sql.DB` does not stop the shared host.

The synthetic ABI fixtures live in `obproxy/testdata`; the host source stays here.
Run their tests with:

```sh
go test -tags=obproxy,obproxytest ./obproxy -count=1
```

Windows amd64 uses `-tags=obproxytest` for synthetic host and library fixtures.
These local fixtures do not establish real OceanBase/OBProxy interoperability.
