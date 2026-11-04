package keychain

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"reflect"
	"slices"
	"testing"

	"github.com/zalando/go-keyring"
)

// fakeBackend mimics go-keyring over a map and counts calls, so tests can
// prove a guard stopped a request before it would have reached security(1).
type fakeBackend struct {
	items map[[2]string]string
	calls int
	err   error
}

func newFake() *fakeBackend { return &fakeBackend{items: map[[2]string]string{}} }

func (f *fakeBackend) Get(service, name string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.items[[2]string{service, name}]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) Set(service, name, value string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.items[[2]string{service, name}] = value
	return nil
}

func (f *fakeBackend) Delete(service, name string) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	k := [2]string{service, name}
	if _, ok := f.items[k]; !ok {
		return keyring.ErrNotFound
	}
	delete(f.items, k)
	return nil
}

// List returns names in map order, leaving the sorting to the Store.
func (f *fakeBackend) List(service string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var names []string
	for k := range maps.Keys(f.items) {
		if k[0] == service {
			names = append(names, k[1])
		}
	}
	return names, nil
}

// exitErr returns a real *exec.ExitError, the shape go-keyring passes through
// when security(1) exits non-zero.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var xerr *exec.ExitError
	if !errors.As(err, &xerr) || xerr.ExitCode() != code {
		t.Fatalf("exit %d: got %v", code, err)
	}
	return err
}

func TestMapErr(t *testing.T) {
	if err := mapErr("X", nil); err != nil {
		t.Fatalf("mapErr(nil) = %v", err)
	}
	cases := []struct {
		desc     string
		err      error
		denied   bool
		notFound bool
	}{
		{"exit 51 auth failed", exitErr(t, 51), true, false},
		{"exit 128 user canceled", exitErr(t, 128), true, false},
		{"exit 36 interaction not allowed", exitErr(t, 36), true, false},
		{"exit 44 item not found", exitErr(t, 44), false, true},
		{"keyring.ErrNotFound", keyring.ErrNotFound, false, true},
		{"other exit code", exitErr(t, 1), false, false},
		{"other error", errors.New("boom"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := mapErr("X", tc.err)
			if got == nil {
				t.Fatal("mapErr returned nil")
			}
			if errors.Is(got, ErrDenied) != tc.denied {
				t.Errorf("errors.Is(%v, ErrDenied) = %v, want %v", got, !tc.denied, tc.denied)
			}
			if errors.Is(got, ErrNotFound) != tc.notFound {
				t.Errorf("errors.Is(%v, ErrNotFound) = %v, want %v", got, !tc.notFound, tc.notFound)
			}
		})
	}
}

func TestDarwinStoreGuards(t *testing.T) {
	f := newFake()
	noService := &darwinStore{service: "", kc: f}
	if _, err := noService.Get("A"); err == nil {
		t.Error("Get with empty service succeeded")
	}
	if err := noService.Set("A", "v"); err == nil {
		t.Error("Set with empty service succeeded")
	}
	if err := noService.Delete("A"); err == nil {
		t.Error("Delete with empty service succeeded")
	}
	if _, err := noService.List(); err == nil {
		t.Error("List with empty service succeeded")
	}

	s := &darwinStore{service: "svc", kc: f}
	if _, err := s.Get(""); err == nil {
		t.Error("Get with empty name succeeded")
	}
	if err := s.Set("", "v"); err == nil {
		t.Error("Set with empty name succeeded")
	}
	if err := s.Delete(""); err == nil {
		t.Error("Delete with empty name succeeded")
	}
	if f.calls != 0 {
		t.Errorf("backend called %d times, want 0", f.calls)
	}
}

func TestDarwinStoreWithFakeBackend(t *testing.T) {
	exerciseStore(t, &darwinStore{service: "svc", kc: newFake()})
}

func TestDarwinStoreMapsBackendErrors(t *testing.T) {
	f := newFake()
	f.err = exitErr(t, 51)
	s := &darwinStore{service: "svc", kc: f}
	if _, err := s.Get("A"); !errors.Is(err, ErrDenied) {
		t.Errorf("Get: got %v, want ErrDenied", err)
	}
	if err := s.Set("A", "v"); !errors.Is(err, ErrDenied) {
		t.Errorf("Set: got %v, want ErrDenied", err)
	}
	if err := s.Delete("A"); !errors.Is(err, ErrDenied) {
		t.Errorf("Delete: got %v, want ErrDenied", err)
	}
	if _, err := s.List(); !errors.Is(err, ErrDenied) {
		t.Errorf("List: got %v, want ErrDenied", err)
	}
}

// dumpSample follows the layout of `security dump-keychain` without -d.
const dumpSample = `keychain: "/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    0x00000007 <blob>="maple"
    0x00000008 <blob>=<NULL>
    "acct"<blob>="B_KEY"
    "cdat"<timedate>=0x32303236313130343030303030305A00  "20261104000000Z\000"
    "svce"<blob>="maple"
    "type"<uint32>=<NULL>
keychain: "/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    "acct"<blob>="OTHER_SERVICE"
    "svce"<blob>="maple-test"
keychain: "/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: "inet"
attributes:
    "acct"<blob>="INTERNET_ITEM"
    "srvr"<blob>="maple"
    "svce"<blob>="maple"
keychain: "/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    "acct"<blob>=0x415C42  "A\134B"
    "svce"<blob>="maple"
keychain: "/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    "acct"<blob>="A_KEY"
    "svce"<blob>=0x6D61706C650A  "maple\012"
keychain:"/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    "acct"<blob>=<NULL>
    "svce"<blob>="maple"
keychain: "/Users/example/Library/Keychains/login.keychain-db"
version: 512
class: 0x0000000F
attributes:
    0x00000001 <blob>="key"
keychain: "/Library/Keychains/System.keychain"
version: 512
class: "genp"
attributes:
    "acct"<blob>="LAST_KEY"
    "svce"<blob>="maple"
`

func TestParseDump(t *testing.T) {
	got := parseDump([]byte(dumpSample), "maple")
	slices.Sort(got)
	// A_KEY is excluded: its service is "maple\n", visible only in the hex form.
	if want := []string{`A\B`, "B_KEY", "LAST_KEY"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDump = %v, want %v", got, want)
	}
	if got := parseDump(nil, "maple"); len(got) != 0 {
		t.Fatalf("parseDump(empty) = %v, want none", got)
	}
}
