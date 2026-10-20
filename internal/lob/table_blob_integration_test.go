//go:build integration

package lob_test

import (
	os "os"
	testing "testing"

	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestTableBlob(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestTableBlobProfile(t, p) })
	}
}
