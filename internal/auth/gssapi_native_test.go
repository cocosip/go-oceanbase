package auth

import (
	"bytes"
	"errors"
	"testing"
	"unsafe"
)

const (
	gssImportStage            = "import"
	gssAcquireStage           = "acquire"
	gssEmptyContinuationStage = "empty-continuation"
	gssOversizedOutputStage   = "oversized-output"
)

type gssNativeFixture struct {
	functions                                                                      gssFunctions
	name, credential, context                                                      byte
	output                                                                         []byte
	imports, acquisitions, steps, buffers, names, credentials, contexts, libraries int
	importStatus, acquireStatus, stepStatus, flags                                 uint32
}

func newGSSNativeFixture(t *testing.T) *gssNativeFixture {
	t.Helper()
	f := &gssNativeFixture{output: []byte{0x60, 0, 0xff}, stepStatus: 1, flags: 2}
	f.functions.importName = func(_ *uint32, input *gssBuffer, oid *gssOID, out *unsafe.Pointer) uint32 {
		f.imports++
		if string(unsafe.Slice(input.value, input.length)) != "service/db.example" || !bytes.Equal(unsafe.Slice(oid.elements, oid.length), []byte{42, 134, 72, 134, 247, 18, 1, 2, 1, 1}) {
			t.Error("native principal or user-name OID changed")
		}
		*out = unsafe.Pointer(&f.name)
		return f.importStatus
	}
	f.functions.acquireCredential = func(_ *uint32, name unsafe.Pointer, _ uint32, mechs *gssOIDSet, usage int32, out *unsafe.Pointer, _ unsafe.Pointer, _ *uint32) uint32 {
		f.acquisitions++
		if name != nil || mechs.count != 1 || usage != 1 || !bytes.Equal(unsafe.Slice(mechs.elements.elements, mechs.elements.length), []byte{42, 134, 72, 134, 247, 18, 1, 2, 2}) {
			t.Error("credential acquisition did not request current Kerberos initiator")
		}
		*out = unsafe.Pointer(&f.credential)
		return f.acquireStatus
	}
	f.functions.initContext = func(_ *uint32, credential unsafe.Pointer, context *unsafe.Pointer, name unsafe.Pointer, _ *gssOID, flags, _ uint32, bindings unsafe.Pointer, input *gssBuffer, _ *unsafe.Pointer, output *gssBuffer, returnedFlags, _ *uint32) uint32 {
		f.steps++
		if credential != unsafe.Pointer(&f.credential) || name != unsafe.Pointer(&f.name) || flags != 2 || bindings != nil {
			t.Error("native credential, target or mutual-auth request changed")
		}
		if f.steps == 1 && (input != nil || *context != nil) {
			t.Error("first native step received an input/context")
		}
		if f.steps > 1 && (*context != unsafe.Pointer(&f.context) || input == nil || !bytes.Equal(unsafe.Slice(input.value, input.length), []byte{0xff, 1, 2})) {
			t.Error("continuation context/token not preserved")
		}
		*context = unsafe.Pointer(&f.context)
		*returnedFlags = f.flags
		if len(f.output) != 0 {
			*output = gssBuffer{length: uintptr(len(f.output)), value: &f.output[0]}
		}
		return f.stepStatus
	}
	f.functions.releaseBuffer = func(_ *uint32, b *gssBuffer) uint32 { f.buffers++; *b = gssBuffer{}; return 0 }
	f.functions.releaseName = func(_ *uint32, p *unsafe.Pointer) uint32 { f.names++; *p = nil; return 0 }
	f.functions.releaseCredential = func(_ *uint32, p *unsafe.Pointer) uint32 { f.credentials++; *p = nil; return 0 }
	f.functions.deleteContext = func(_ *uint32, p *unsafe.Pointer, out *gssBuffer) uint32 {
		f.contexts++
		*p = nil
		if out != nil {
			t.Error("context deletion requested a network token")
		}
		return 0
	}
	return f
}

func (f *gssNativeFixture) closeLibrary() error { f.libraries++; return nil }

func TestAuthPluginGSSNativeConversation(t *testing.T) {
	f := newGSSNativeFixture(t)
	c, err := newNativeGSSContext(&f.functions, f.closeLibrary, testGSSPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	out, done, err := c.Step(nil)
	if err != nil || done || !bytes.Equal(out, []byte{0x60, 0, 0xff}) || f.buffers != 1 {
		t.Fatal("first token", out, done, err)
	}
	f.output[0] = 42
	if out[0] != 0x60 {
		t.Fatal("native token memory escaped release")
	}
	f.stepStatus, f.output = 0, nil
	out, done, err = c.Step([]byte{0xff, 1, 2})
	if err != nil || !done || out != nil {
		t.Fatal("mutual completion", out, done, err)
	}
	if _, _, err = c.Step([]byte{1}); err == nil {
		t.Fatal("completed context reused")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if f.names != 1 || f.credentials != 1 || f.contexts != 1 || f.libraries != 1 {
		t.Fatalf("resource counts: %+v", f)
	}
}

func TestAuthPluginGSSNativeFailureCleanup(t *testing.T) {
	for _, stage := range []string{gssImportStage, gssAcquireStage, "step", "supplementary", "mutual", gssEmptyContinuationStage, gssOversizedOutputStage, "nil-output"} {
		t.Run(stage, func(t *testing.T) {
			f := newGSSNativeFixture(t)
			switch stage {
			case gssImportStage:
				f.importStatus = 1 << 16
			case gssAcquireStage:
				f.acquireStatus = 1 << 16
			case "step":
				f.stepStatus = 1 << 16
			case "supplementary":
				f.stepStatus = 3
			case "mutual":
				f.stepStatus, f.flags = 0, 0
			case gssEmptyContinuationStage:
				f.output = nil
			case gssOversizedOutputStage, "nil-output":
				original := f.functions.initContext
				f.functions.initContext = func(minor *uint32, cred unsafe.Pointer, ctx *unsafe.Pointer, name unsafe.Pointer, oid *gssOID, flags, life uint32, bindings unsafe.Pointer, input *gssBuffer, mech *unsafe.Pointer, output *gssBuffer, returned, expires *uint32) uint32 {
					status := original(minor, cred, ctx, name, oid, flags, life, bindings, input, mech, output, returned, expires)
					if stage == gssOversizedOutputStage {
						output.length = 16<<20 + 1
					} else {
						output.value = nil
					}
					return status
				}
			}
			c, err := newNativeGSSContext(&f.functions, f.closeLibrary, testGSSPrincipal)
			if stage == gssImportStage || stage == gssAcquireStage {
				if err == nil || c != nil || f.names != 1 || f.libraries != 1 || (stage == gssAcquireStage && f.credentials != 1) {
					t.Fatal("constructor failure leaked native handles", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = c.Step(nil); err == nil {
				t.Fatal("accepted invalid native result")
			}
			if _, _, err = c.Step([]byte{1}); err == nil || f.steps != 1 {
				t.Fatal("failed native context reused")
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			wantBuffers := 1
			if stage == gssEmptyContinuationStage {
				wantBuffers = 0
			}
			if f.contexts != 1 || f.names != 1 || f.credentials != 1 || f.libraries != 1 || f.buffers != wantBuffers {
				t.Fatal("step failure leaked native resources", f.contexts, f.names, f.credentials, f.libraries, f.buffers)
			}
		})
	}
}

func TestAuthPluginGSSNativeInputAndClose(t *testing.T) {
	f := newGSSNativeFixture(t)
	c, err := newNativeGSSContext(&f.functions, f.closeLibrary, testGSSPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.Step([]byte{1}); !errors.Is(err, ErrInvalidChallenge) || f.steps != 0 {
		t.Fatal("initial input accepted", err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.Step(nil); !errors.Is(err, ErrInvalidChallenge) {
		t.Fatal("closed context reused", err)
	}
	if f.contexts != 0 || f.names != 1 || f.credentials != 1 || f.libraries != 1 {
		t.Fatal("unused context cleanup")
	}
}

func TestAuthPluginGSSNativeInvalidPrincipal(t *testing.T) {
	f := newGSSNativeFixture(t)
	c, err := newNativeGSSContext(&f.functions, f.closeLibrary, "bad\x00principal")
	if c != nil || !errors.Is(err, ErrInvalidChallenge) || f.imports != 0 || f.acquisitions != 0 || f.libraries != 1 {
		t.Fatal("invalid principal reached native identity operations", c, err)
	}
}

func TestAuthPluginGSSNativeContinuationBounds(t *testing.T) {
	f := newGSSNativeFixture(t)
	c, err := newNativeGSSContext(&f.functions, f.closeLibrary, testGSSPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.Step(nil); err != nil {
		t.Fatal(err)
	}
	for _, token := range [][]byte{nil, make([]byte, 16<<20+1)} {
		if _, _, err = c.Step(token); !errors.Is(err, ErrInvalidChallenge) || f.steps != 1 {
			t.Fatal("invalid continuation reached native GSSAPI", len(token), f.steps, err)
		}
	}
	if err = c.Close(); err != nil || f.contexts != 1 || f.libraries != 1 {
		t.Fatal("invalid continuation lost cleanup", err)
	}
}
