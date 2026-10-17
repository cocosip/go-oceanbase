//go:build gssapi && ((linux && !android && (amd64 || arm64 || 386)) || (darwin && !ios && (amd64 || arm64)))

package auth

import (
	"errors"
	"runtime"

	"github.com/ebitengine/purego"
)

// NewGSSContext uses system GSSAPI and the process's default Kerberos cache.
func NewGSSContext(spn, mechanism string) (GSSContext, error) {
	if !ValidGSSPrincipal(spn) {
		return nil, ErrInvalidChallenge
	}
	if mechanism != "Kerberos" {
		return nil, ErrGSSMechanism
	}
	libraries := []string{"libgssapi_krb5.so.2", "libgssapi.so.3", "libgssapi.so"}
	if runtime.GOOS == "darwin" {
		libraries = []string{"/System/Library/Frameworks/GSS.framework/GSS"}
	}
	var failures error
	for _, library := range libraries {
		lookup, closeLibrary, err := openGSSLibrary(library)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		api, err := bindGSSFunctions(lookup)
		if err != nil {
			return nil, errors.Join(ErrGSSUnavailable, err, closeLibrary())
		}
		return newNativeGSSContext(api, closeLibrary, spn)
	}
	return nil, errors.Join(ErrGSSUnavailable, failures)
}

func openGSSLibrary(path string) (func(string) (uintptr, error), func() error, error) {
	handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, nil, err
	}
	return func(name string) (uintptr, error) { return purego.Dlsym(handle, name) }, func() error { return purego.Dlclose(handle) }, nil
}
