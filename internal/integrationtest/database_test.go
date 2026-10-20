package integrationtest

import (
	context "context"
	errors "errors"
	fmt "fmt"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func TestDuplicateKeyErrors(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, code := range []uint32{0, 1, 1062, 1064, 1031, 942} {
			want := mode == oceanbase.ModeMySQL && code == 1062 || mode == oceanbase.ModeOracle && code == 1
			server := &oceanbase.Error{Code: code}
			for _, err := range []error{server, fmt.Errorf("wrapped: %w", server), &oceanbase.BatchError{Index: 1, Err: server}, errors.Join(server, server)} {
				if DuplicateKey(err, mode) != want {
					t.Errorf("mode=%d code=%d: duplicate classification mismatch", mode, code)
				}
			}
		}
		for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, (*oceanbase.Error)(nil)} {
			if DuplicateKey(err, mode) {
				t.Errorf("mode=%d: non-server error accepted as duplicate", mode)
			}
		}
		for _, code := range []uint32{1, 1062} {
			if DuplicateKey(errors.Join(&oceanbase.Error{Code: code}, context.DeadlineExceeded), mode) {
				t.Error("joined unrelated failure accepted as duplicate")
			}
		}
	}
}

func TestExpectedServerErrors(t *testing.T) {
	for _, code := range []uint32{1, 1062, 1644, 20001, 1317, 1013} {
		server := &oceanbase.Error{Code: code}
		other := &oceanbase.Error{Code: code + 1}
		for _, tc := range []struct {
			name string
			err  error
			want bool
		}{
			{"direct", server, true},
			{"wrapped", fmt.Errorf("wrapped: %w", server), true},
			{"joined-matching", errors.Join(server, server), true},
			{"nested-matching", fmt.Errorf("wrapped: %w", errors.Join(server, fmt.Errorf("nested: %w", server))), true},
			{"nil", nil, false},
			{"typed-nil", (*oceanbase.Error)(nil), false},
			{"timeout", context.DeadlineExceeded, false},
			{"other-code", other, false},
			{"joined-timeout", errors.Join(server, context.DeadlineExceeded), false},
			{"joined-cleanup", errors.Join(server, errors.New("cleanup failed")), false},
			{"joined-other-code", errors.Join(server, other), false},
			{"joined-other-first", errors.Join(other, server), false},
			{"joined-typed-nil", errors.Join(server, (*oceanbase.Error)(nil)), false},
			{"wrapped-joined-timeout", fmt.Errorf("wrapped: %w", errors.Join(server, context.DeadlineExceeded)), false},
		} {
			t.Run(fmt.Sprintf("%d/%s", code, tc.name), func(t *testing.T) {
				if got := OnlyServerErrorCode(tc.err, code); got != tc.want {
					t.Fatalf("server error classification = %v; want %v", got, tc.want)
				}
			})
		}
	}
}

func TestProfiles(t *testing.T) {
	all, err := Profiles("all")
	if err != nil || len(all) != 4 {
		t.Fatal("profile matrix incomplete")
	}
	for _, name := range []string{"mysql", "oracle", "proxy-mysql", "proxy-oracle"} {
		items, err := Profiles(name)
		if err != nil || len(items) != 1 || items[0].Name != name || items[0].Env == "" {
			t.Fatal("incorrect profile selection")
		}
	}
	if _, err = Profiles("unexpected"); err == nil {
		t.Fatal("invalid profile silently selected")
	}
}

func TestXAFlagRejectionClassification(t *testing.T) {
	server := &oceanbase.Error{Code: 1398}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{errors.Join(oceanbase.ErrXA, &oceanbase.XAError{Code: -5, Err: server}), true},
		{server, true}, {oceanbase.ErrXA, false}, {nil, false},
		{errors.Join(oceanbase.ErrXA, server, context.DeadlineExceeded), false},
		{errors.Join(oceanbase.ErrXA, server, errors.New("cleanup")), false},
		{errors.Join(oceanbase.ErrXA, &oceanbase.Error{Code: 1064}), false},
		{errors.Join(oceanbase.ErrXA, server, &oceanbase.Error{Code: 1142}), false},
	} {
		if XAFlagRejected(tc.err, oceanbase.ModeMySQL) != tc.want {
			t.Errorf("classification mismatch: %v", tc.err)
		}
		if XAFlagRejected(tc.err, oceanbase.ModeOracle) {
			t.Error("Oracle error classified as MySQL flag rejection")
		}
	}
}
