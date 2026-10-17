//go:build windows && amd64 && gssapitest

package auth

import "golang.org/x/sys/windows"

func openGSSLibrary(path string) (func(string) (uintptr, error), func() error, error) {
	library, err := windows.LoadDLL(path)
	if err != nil {
		return nil, nil, err
	}
	return func(name string) (uintptr, error) {
		proc, e := library.FindProc(name)
		if e != nil {
			return 0, e
		}
		return proc.Addr(), nil
	}, library.Release, nil
}
