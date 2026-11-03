package manage

import (
	"context"
	"errors"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

func TestStatus(t *testing.T) {
	e := setup(t, "")
	if _, err := Status(context.Background(), e); !errors.Is(err, ErrGatewayDown) {
		t.Fatalf("down err = %v", err)
	}
	fakeGateway(t, e, func() config.Status {
		return config.Status{Version: "v1", ConfigHash: "sha256:x"}
	})
	st, err := Status(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "v1" || st.ConfigHash != "sha256:x" {
		t.Errorf("status = %+v", st)
	}
}

func TestStatusWrongToken(t *testing.T) {
	e := setup(t, "")
	fakeGateway(t, e, func() config.Status { return config.Status{} })
	writeFile(t, e.Paths.Token(), "other\n")
	_, err := Status(context.Background(), e)
	if err == nil || errors.Is(err, ErrGatewayDown) || !contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}
