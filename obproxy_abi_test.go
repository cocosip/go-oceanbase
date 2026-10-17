//go:build obproxytest && ((obproxy && linux && !android && (amd64 || arm64)) || (windows && amd64))

package oceanbase

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const obProxyFixtureEnv = "OB_TEST_VC_FIXTURE"
const obProxyHostEnv = "OB_TEST_VC_HOST"
const obProxyRecordEnv = "OB_TEST_VC_RECORD"
const obProxyModeEnv = "OB_TEST_VC_MODE"

func buildOBProxyFixture(t *testing.T, source, name string, shared bool, extra ...string) string {
	t.Helper()
	compiler, err := exec.LookPath("gcc")
	if err != nil {
		t.Fatal("obproxytest requires gcc:", err)
	}
	ext := ""
	isWindows := runtime.GOOS == "windows"
	args := []string{"-std=c11", "-O2", "-Wall", "-Wextra", "-Werror"}
	if shared {
		ext = ".so"
		args = append(args, "-shared")
		if !isWindows {
			args = append(args, "-fPIC")
		}
	}
	if isWindows {
		ext = ".exe"
		if shared {
			ext = ".dll"
		}
	}
	output := filepath.Join(t.TempDir(), name+ext)
	args = append(args, extra...)
	args = append(args, "-o", output, source)
	if !isWindows && !shared {
		args = append(args, "-ldl", "-pthread")
	}
	if data, e := exec.Command(compiler, args...).CombinedOutput(); e != nil {
		t.Fatalf("C build: %s: %v", data, e)
	}
	return output
}

func obProxyFixtureEnvironment(mode int, record string) []string {
	return append(os.Environ(), obProxyModeEnv+"="+strconv.Itoa(mode), obProxyRecordEnv+"="+record)
}

func obProxyFrame(config string) []byte {
	data := binary.LittleEndian.AppendUint32(nil, uint32(len(config)))
	return append(data, config...)
}

func TestOBProxyNativeABI(t *testing.T) {
	if library := os.Getenv(obProxyFixtureEnv); library != "" {
		checkOBProxyNativeProcess(t, os.Getenv(obProxyHostEnv), library)
		return
	}
	host := buildOBProxyFixture(t, "tools/obproxy-host/main.c", "proxy-host", false)
	library := buildOBProxyFixture(t, "testdata/obproxy_fixture.c", "proxy-fixture", true)
	missing := buildOBProxyFixture(t, "testdata/obproxy_fixture.c", "missing-close", true, "-DOB_TEST_MISSING_CLOSE")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for mode := range 7 {
		t.Run(strconv.Itoa(mode), func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "record")
			lib := library
			if mode == 5 {
				lib = missing
			}
			if mode == 6 {
				lib += ".missing"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestOBProxyNativeABI$", "-test.count=1", "-test.timeout=20s")
			child.Env = append(obProxyFixtureEnvironment(mode, record), obProxyFixtureEnv+"="+lib, obProxyHostEnv+"="+host)
			if output, e := child.CombinedOutput(); e != nil {
				data, _ := os.ReadFile(record)
				t.Fatalf("native host mode %d: %s: %v; record=%q", mode, output, e, data)
			}
			if mode >= 5 {
				if _, e := os.Stat(record); !os.IsNotExist(e) {
					t.Fatal("invalid library invoked native init", e)
				}
				return
			}
			data, e := os.ReadFile(record)
			if e != nil {
				t.Fatal(e)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			var config map[string]string
			if e = json.Unmarshal([]byte(lines[0]), &config); e != nil || len(config) != 1 || config["proxy_config"] != "x=\"y\\z\"\n\t\x01中文" {
				t.Fatalf("raw -o value did not survive native JSON: %q: %v", data, e)
			}
			wantClosed := mode == 0 || mode == 3 || mode == 4
			if (len(lines) == 2 && lines[1] == "close") != wantClosed || len(lines) > 2 {
				t.Fatalf("temporary client cleanup or singleton broken: %q", data)
			}
		})
	}
}

func checkOBProxyNativeProcess(t *testing.T, host, library string) {
	t.Helper()
	directory, err := os.MkdirTemp("", "vc-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(directory) }()
	t.Chdir(directory)
	if err = os.Mkdir(".obproxy", 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(".obproxy", "obproxy.domain")
	listener, err := net.Listen(networkUnix, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	dial, err := NewOBProxyDialer(host, library, "x=\"y\\z\"\n\t\x01中文")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	success := os.Getenv(obProxyModeEnv) == "0"
	for range 2 {
		c, e := dial(ctx, networkUnix, socket)
		if (e == nil) != success {
			t.Fatal("native initialization status lost", e)
		}
		if !success {
			want := map[string]string{"1": "status -4000", "2": "status -2", "3": "status -4000", "4": "status -4201", "5": "status -1", "6": "status -1"}[os.Getenv(obProxyModeEnv)]
			if !strings.Contains(e.Error(), want) {
				t.Fatal("native error replaced by transport or cancellation", e)
			}
			continue
		}
		_ = c.Close()
		s, e := listener.Accept()
		if e != nil {
			t.Fatal(e)
		}
		_ = s.Close()
	}
}

// These tests run the actual C host and require it to exit on control EOF even
// while vendor initialization is blocked; a context alone cannot stop C threads.
func TestOBProxyHostParentEOF(t *testing.T) {
	host := buildOBProxyFixture(t, "tools/obproxy-host/main.c", "proxy-host", false)
	library := buildOBProxyFixture(t, "testdata/obproxy_fixture.c", "proxy-fixture", true)
	for _, mode := range []int{0, 5} {
		t.Run(strconv.Itoa(mode), func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "record")
			cmd := exec.Command(host, library)
			cmd.Env = obProxyFixtureEnvironment(mode, record)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
			if _, err = input.Write(obProxyFrame("")); err != nil {
				t.Fatal(err)
			}
			if mode == 0 {
				var ready [8]byte
				if _, err = io.ReadFull(output, ready[:]); err != nil || string(ready[:]) != "OBV1\x00\x00\x00\x00" {
					t.Fatal("host readiness", ready, err)
				}
			} else {
				poolAwait(t, func() bool {
					data, e := os.ReadFile(record)
					return e == nil && bytes.HasPrefix(data, []byte("{}\n"))
				})
			}
			if err = input.Close(); err != nil {
				t.Fatal(err)
			}
			go func() { done <- cmd.Wait() }()
			select {
			case err = <-done:
				if err != nil {
					t.Fatal("host EOF exit", err)
				}
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("orphan native host after parent EOF")
			}
			data, e := os.ReadFile(record)
			if e != nil || !bytes.HasPrefix(data, []byte("{}\n")) {
				t.Fatalf("empty native config: %q: %v", data, e)
			}
		})
	}
}

func TestOBProxyHostInvalidConfig(t *testing.T) {
	host := buildOBProxyFixture(t, "tools/obproxy-host/main.c", "proxy-host", false)
	for _, data := range [][]byte{obProxyFrame(strings.Repeat("x", 4096)), obProxyFrame("x\x00y"), {1, 0, 0, 0}} {
		cmd := exec.Command(host, "not-loaded")
		cmd.Stdin = bytes.NewReader(data)
		if output, err := cmd.CombinedOutput(); err == nil || len(output) != 0 {
			t.Fatalf("host accepted invalid frame: %q: %v", output, err)
		}
	}
}

func TestOBProxyHostMissingExecutable(t *testing.T) {
	spec := obProxyTestSpec(t)
	if err := initializeOBProxy(spec); err == nil {
		t.Fatal("missing host accepted")
	}
}

func TestOBProxyHostMaximumConfig(t *testing.T) {
	host := buildOBProxyFixture(t, "tools/obproxy-host/main.c", "proxy-host", false)
	library := buildOBProxyFixture(t, "testdata/obproxy_fixture.c", "proxy-fixture", true)
	record := filepath.Join(t.TempDir(), "record")
	config := strings.Repeat("\x01", 4095)
	cmd := exec.Command(host, library)
	cmd.Env = obProxyFixtureEnvironment(1, record)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	if _, err = input.Write(obProxyFrame(config)); err != nil {
		t.Fatal(err)
	}
	var ready [8]byte
	if _, err = io.ReadFull(output, ready[:]); err != nil || int32(binary.LittleEndian.Uint32(ready[4:])) != -4000 {
		t.Fatal("host maximum config", ready, err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err = json.Unmarshal(data, &decoded); err != nil || decoded["proxy_config"] != config {
		t.Fatal("maximum escaped config truncated", err)
	}
}

func TestOBProxyHostReadiness(t *testing.T) {
	host := buildOBProxyFixture(t, "testdata/obproxy_bad_host.c", "bad-host", false)
	spec := obProxyTestSpec(t)
	spec.host = host
	for _, mode := range []string{"bad", "short", "status"} {
		spec.library = mode
		for range 40 {
			err := initializeOBProxy(spec)
			if err == nil {
				t.Fatal("invalid host handshake accepted", mode)
			}
			if mode == "status" && !strings.Contains(err.Error(), "status -4000") {
				t.Fatal("native status lost when host exits:", err)
			}
		}
	}
}
