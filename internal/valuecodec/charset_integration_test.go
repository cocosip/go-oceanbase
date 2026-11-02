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
				{"sjis", "日本語ｱ〜;\\'"}, {"big5", "中文;\\'"}, {"gb2312", "中文;\\'"},
				{"euckr", "한글;\\'"}, {"dec8", "ŒŸœÿ;\\'"}, {"tis620", "กข฿;\\'"},
			} {
				t.Run(tc.charset, func(t *testing.T) { integrationTestMySQLCharset(t, p, tc.charset, tc.text) })
			}
		})
	}
}

func TestMySQLUTF16LEResult(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, p)
			ctx := integrationtest.TestContext(t)
			for _, want := range []string{"中😀", "", "\ufffd"} {
				var got string
				if err := db.QueryRowContext(ctx, "SELECT CONVERT(? USING utf16le)", want).Scan(&got); err != nil {
					integrationtest.FailOperation(t, "UTF16LE result", err)
				}
				if got != want {
					t.Fatalf("UTF16LE result: %q, want %q", got, want)
				}
			}
		})
	}
}
