package manage

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

func TestTestCommand(t *testing.T) {
	bin := testutil.BuildFakeMCP(t)
	e := setup(t, "servers:\n  fake:\n    command: "+bin+"\n    env:\n      TOKEN: ${FAKE_TOKEN}\n")
	_ = e.Keychain.Set("FAKE_TOKEN", "v")
	res, err := Test(context.Background(), e, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if res.Server != "fake" || len(res.Tools) == 0 || !slices.IsSorted(res.Tools) {
		t.Errorf("res = %+v", res)
	}
	if _, err := Test(context.Background(), e, "nope"); err == nil {
		t.Error("unknown server: no error")
	}
}

func TestTestRedactsSecrets(t *testing.T) {
	e := setup(t, "servers:\n  r:\n    url: http://127.0.0.1:1/mcp?key=${R_KEY}\n")
	_ = e.Keychain.Set("R_KEY", "sekrit123")
	_, err := Test(context.Background(), e, "r")
	if err == nil || strings.Contains(err.Error(), "sekrit123") {
		t.Fatalf("err = %v", err)
	}
}
