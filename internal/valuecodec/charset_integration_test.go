//go:build integration

package valuecodec_test

import (
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestMySQLCharsets(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			for _, tc := range []struct{ charset, text string }{
				{"utf8mb4", "中文🙂"}, {"utf8", "中文"}, {"latin1", "café €"},
				{"gbk", "中文"}, {"gb18030", "中文🙂"}, {"ascii", "ascii;\\'"},
			} {
				t.Run(tc.charset, func(t *testing.T) { integrationTestMySQLCharset(t, p, tc.charset, tc.text) })
			}
		})
	}
}
