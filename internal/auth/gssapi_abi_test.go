//go:build gssapitest && ((gssapi && ((linux && !android && (amd64 || arm64 || 386)) || (darwin && !ios && (amd64 || arm64)))) || (windows && amd64))

package auth

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ebitengine/purego"
)

func TestAuthPluginGSSNativeABI(t *testing.T) {
	compiler, err := exec.LookPath("cc")
	if err != nil {
		compiler, err = exec.LookPath("gcc")
	}
	if err != nil {
		t.Fatal("gssapitest requires a C compiler; driver builds do not")
	}
	ext := ".so"
	switch runtime.GOOS {
	case "windows":
		ext = ".dll"
	case "darwin":
		ext = ".dylib"
	}
	library := filepath.Join(t.TempDir(), "gssapi_fixture"+ext)
	args := []string{"-shared", "-O2", "-o", library, "testdata/gssapi_fixture.c"}
	if runtime.GOOS != "windows" {
		args = append(args, "-fPIC")
	}
	if runtime.GOARCH == "386" {
		args = append(args, "-m32")
	}
	if output, e := exec.Command(compiler, args...).CombinedOutput(); e != nil {
		t.Fatalf("C fixture: %s: %v", output, e)
	}
	lookup, release, err := openGSSLibrary(library)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := release(); e != nil {
			t.Error(e)
		}
	})
	var setMode func(int32)
	var live func() uint32
	for _, entry := range []struct {
		name   string
		target any
	}{{"gss_test_mode", &setMode}, {"gss_test_live", &live}} {
		address, e := lookup(entry.name)
		if e != nil {
			t.Fatal(e)
		}
		purego.RegisterFunc(entry.target, address)
	}
	for mode := int32(0); mode <= 6; mode++ {
		setMode(mode)
		api, e := bindGSSFunctions(lookup)
		if e != nil {
			t.Fatal(e)
		}
		checkGSSNativeABIMode(t, api, live, mode)
	}
}

func checkGSSNativeABIMode(t *testing.T, api *gssFunctions, live func() uint32, mode int32) {
	t.Helper()
	closed := 0
	c, e := newNativeGSSContext(api, func() error { closed++; return nil }, testGSSPrincipal)
	if mode == 1 || mode == 2 {
		if e == nil || c != nil || live() != 0 || closed != 1 {
			t.Fatalf("native constructor mode %d leaked: live=%d closed=%d err=%v", mode, live(), closed, e)
		}
		return
	}
	if e != nil {
		t.Fatal(e)
	}
	out, complete, e := c.Step(nil)
	if mode == 3 || mode == 5 {
		if e == nil {
			t.Fatalf("native failure %d accepted", mode)
		}
	} else {
		if e != nil || complete || !bytes.Equal(out, []byte{0x60, 0, 0xff}) {
			t.Fatal("native ABI initial token", mode, out, complete, e)
		}
		out, complete, e = c.Step([]byte{0xff, 1, 2})
		if mode == 4 {
			if e == nil {
				t.Fatal("native non-mutual context accepted")
			}
		} else if e != nil || !complete || out != nil {
			t.Fatal("native ABI completion", mode, out, complete, e)
		}
	}
	e = c.Close()
	if mode == 6 {
		if e == nil || !strings.Contains(e.Error(), "delete context") || !strings.Contains(e.Error(), "release credential") || !strings.Contains(e.Error(), "release name") {
			t.Fatal("native cleanup errors lost", e)
		}
	} else if e != nil {
		t.Fatal(e)
	}
	if live() != 0 || closed != 1 {
		t.Fatal("native ABI leaked resources", mode, live(), closed)
	}
	if e = c.Close(); e != nil || closed != 1 {
		t.Fatal("native ABI double close", e)
	}
}

func TestAuthPluginGSSMissingSymbol(t *testing.T) {
	want := errors.New("fixture: symbol unavailable")
	api, err := bindGSSFunctions(func(string) (uintptr, error) { return 0, want })
	if api != nil || !errors.Is(err, want) {
		t.Fatal("missing symbol did not return an error", err)
	}
}
