package auth

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// RFC 2744 uses size_t for buffers/sets and OM_uint32 for OID lengths.
type gssBuffer struct {
	length uintptr
	value  *byte
}

type gssOID struct {
	length   uint32
	elements *byte
}

type gssOIDSet struct {
	count    uintptr
	elements *gssOID
}

type gssFunctions struct {
	importName        func(*uint32, *gssBuffer, *gssOID, *unsafe.Pointer) uint32
	acquireCredential func(*uint32, unsafe.Pointer, uint32, *gssOIDSet, int32, *unsafe.Pointer, unsafe.Pointer, *uint32) uint32
	initContext       func(*uint32, unsafe.Pointer, *unsafe.Pointer, unsafe.Pointer, *gssOID, uint32, uint32, unsafe.Pointer, *gssBuffer, *unsafe.Pointer, *gssBuffer, *uint32, *uint32) uint32
	releaseBuffer     func(*uint32, *gssBuffer) uint32
	releaseName       func(*uint32, *unsafe.Pointer) uint32
	releaseCredential func(*uint32, *unsafe.Pointer) uint32
	deleteContext     func(*uint32, *unsafe.Pointer, *gssBuffer) uint32
}

type nativeGSSContext struct {
	api                       *gssFunctions
	closeLibrary              func() error
	name, credential, context unsafe.Pointer
	started, complete, failed bool
}

var (
	gssUserNameOID = [...]byte{42, 134, 72, 134, 247, 18, 1, 2, 1, 1}
	gssKerberosOID = [...]byte{42, 134, 72, 134, 247, 18, 1, 2, 2}
)

func gssStatus(stage string, major, minor uint32) error {
	if major == 0 {
		return nil
	}
	// Native display strings can contain identities, paths and ticket details.
	return fmt.Errorf("auth: GSSAPI %s failed (major=%08x minor=%08x)", stage, major, minor)
}

func newNativeGSSContext(api *gssFunctions, closeLibrary func() error, spn string) (GSSContext, error) {
	c := &nativeGSSContext{api: api, closeLibrary: closeLibrary}
	if !ValidGSSPrincipal(spn) {
		return nil, errors.Join(ErrInvalidChallenge, c.Close())
	}
	name := []byte(spn)
	input := gssBuffer{length: uintptr(len(name)), value: &name[0]}
	nameOID := gssOID{length: uint32(len(gssUserNameOID)), elements: &gssUserNameOID[0]}
	kerberosOID := gssOID{length: uint32(len(gssKerberosOID)), elements: &gssKerberosOID[0]}
	mechs := gssOIDSet{count: 1, elements: &kerberosOID}
	var pinned runtime.Pinner
	pinned.Pin(&name[0])
	pinned.Pin(&gssUserNameOID[0])
	pinned.Pin(&gssKerberosOID[0])
	pinned.Pin(&kerberosOID)
	defer pinned.Unpin()
	var minor uint32
	major := api.importName(&minor, &input, &nameOID, &c.name)
	if err := gssStatus("import name", major, minor); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	// GSS_C_NO_NAME + INITIATE acquires the process's default cached identity.
	major = api.acquireCredential(&minor, nil, 0, &mechs, 1, &c.credential, nil, nil)
	if err := gssStatus("acquire credential", major, minor); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	if c.name == nil || c.credential == nil {
		return nil, errors.Join(ErrInvalidChallenge, c.Close())
	}
	return c, nil
}

func (c *nativeGSSContext) Step(token []byte) ([]byte, bool, error) {
	if c.closeLibrary == nil || c.complete || c.failed || (c.started && len(token) == 0) || (!c.started && len(token) != 0) || len(token) > 16<<20 {
		return nil, false, ErrInvalidChallenge
	}
	c.started = true
	oid := gssOID{length: uint32(len(gssKerberosOID)), elements: &gssKerberosOID[0]}
	var pinned runtime.Pinner
	pinned.Pin(&gssKerberosOID[0])
	defer pinned.Unpin()
	var input *gssBuffer
	if len(token) != 0 {
		pinned.Pin(&token[0])
		input = &gssBuffer{length: uintptr(len(token)), value: &token[0]}
	}
	var output gssBuffer
	var minor, flags uint32
	// Only GSS_C_MUTUAL_FLAG is requested; delegation is never implicit.
	major := c.api.initContext(&minor, c.credential, &c.context, c.name, &oid, 2, 0, nil, input, nil, &output, &flags, nil)
	complete := major == 0
	var err error
	if major != 0 && major != 1 {
		err = gssStatus("init context", major, minor)
	}
	if output.length > 16<<20 || (output.length != 0 && output.value == nil) || (major == 1 && output.length == 0) {
		err = errors.Join(err, ErrInvalidChallenge)
	}
	if major == 0 && flags&2 == 0 {
		err = errors.Join(err, errors.New("auth: GSSAPI did not establish mutual authentication"))
	}
	var response []byte
	if err == nil && output.length != 0 {
		response = bytes.Clone(unsafe.Slice(output.value, int(output.length)))
	}
	if output.value != nil || output.length != 0 {
		major = c.api.releaseBuffer(&minor, &output)
		err = errors.Join(err, gssStatus("release buffer", major, minor))
	}
	if err != nil {
		c.failed = true
		return nil, false, err
	}
	c.complete = complete
	return response, c.complete, nil
}

func (c *nativeGSSContext) Close() error {
	if c.closeLibrary == nil {
		return nil
	}
	var err error
	var minor uint32
	if c.context != nil {
		major := c.api.deleteContext(&minor, &c.context, nil)
		err = errors.Join(err, gssStatus("delete context", major, minor))
		c.context = nil
	}
	if c.credential != nil {
		major := c.api.releaseCredential(&minor, &c.credential)
		err = errors.Join(err, gssStatus("release credential", major, minor))
		c.credential = nil
	}
	if c.name != nil {
		major := c.api.releaseName(&minor, &c.name)
		err = errors.Join(err, gssStatus("release name", major, minor))
		c.name = nil
	}
	err = errors.Join(err, c.closeLibrary())
	c.closeLibrary = nil
	return err
}
