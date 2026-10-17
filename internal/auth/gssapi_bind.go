//go:build (gssapi && ((linux && !android && (amd64 || arm64 || 386)) || (darwin && !ios && (amd64 || arm64)))) || (windows && amd64 && gssapitest)

package auth

import "github.com/ebitengine/purego"

func bindGSSFunctions(lookup func(string) (uintptr, error)) (*gssFunctions, error) {
	api := &gssFunctions{}
	for _, entry := range []struct {
		name   string
		target any
	}{
		{"gss_import_name", &api.importName},
		{"gss_acquire_cred", &api.acquireCredential},
		{"gss_init_sec_context", &api.initContext},
		{"gss_release_buffer", &api.releaseBuffer},
		{"gss_release_name", &api.releaseName},
		{"gss_release_cred", &api.releaseCredential},
		{"gss_delete_sec_context", &api.deleteContext},
	} {
		symbol, err := lookup(entry.name)
		if err != nil {
			return nil, err
		}
		purego.RegisterFunc(entry.target, symbol)
	}
	return api, nil
}
