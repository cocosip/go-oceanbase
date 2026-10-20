package catalog_test

import (
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func TestMetadataCatalogAndRemarks(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, charset := range []string{testUTF8MB4, localFilenameCharset} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, charset, framing), func(t *testing.T) {
					testMetadataCatalogAndRemarks(t, mode, charset, framing)
				})
			}
		}
	}
}
