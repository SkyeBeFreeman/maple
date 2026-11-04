package keychain

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/zalando/go-keyring"
)

// Items are reached through /usr/bin/security rather than Security.framework
// so their ACL trusts the Apple-signed tool, not maple's ad-hoc signature,
// which changes on every rebuild and would make macOS prompt again for every
// item. The cost: any process of the same user can read them the same way.
const securityPath = "/usr/bin/security"

// security(1) exits with the low byte of the failing OSStatus.
const (
	exitInteractionNotAllowed = 36  // errSecInteractionNotAllowed
	exitItemNotFound          = 44  // errSecItemNotFound
	exitAuthFailed            = 51  // errSecAuthFailed
	exitUserCanceled          = 128 // errSecUserCanceled
)

// errEmptyService guards like errEmptyName: security(1) drops an empty -s from
// the query, so it would match items of every service.
var errEmptyService = errors.New("keychain: empty service")

// New returns a Store backed by generic password items in the user's default
// keychain, with service as the item service and the secret name as account.
func New(service string) Store {
	return &darwinStore{service: service, kc: securityCLI{}}
}

// backend is the seam in front of security(1), so the guards and the error
// mapping can be tested without touching the real keychain.
type backend interface {
	Get(service, name string) (string, error)
	Set(service, name, value string) error
	Delete(service, name string) error
	List(service string) ([]string, error)
}

type darwinStore struct {
	service string
	kc      backend
}

func (d *darwinStore) check(name string) error {
	if d.service == "" {
		return errEmptyService
	}
	if name == "" {
		return errEmptyName
	}
	return nil
}

func (d *darwinStore) Get(name string) (string, error) {
	if err := d.check(name); err != nil {
		return "", err
	}
	v, err := d.kc.Get(d.service, name)
	if err != nil {
		return "", mapErr(name, err)
	}
	return v, nil
}

func (d *darwinStore) Set(name, value string) error {
	if err := d.check(name); err != nil {
		return err
	}
	return mapErr(name, d.kc.Set(d.service, name, value))
}

func (d *darwinStore) Delete(name string) error {
	if err := d.check(name); err != nil {
		return err
	}
	return mapErr(name, d.kc.Delete(d.service, name))
}

func (d *darwinStore) List() ([]string, error) {
	if d.service == "" {
		return nil, errEmptyService
	}
	names, err := d.kc.List(d.service)
	if err != nil {
		return nil, mapErr(d.service, err)
	}
	slices.Sort(names)
	// The same account may exist in more than one keychain of the search list.
	return slices.Compact(names), nil
}

func mapErr(name string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	var xerr *exec.ExitError
	if errors.As(err, &xerr) {
		switch xerr.ExitCode() {
		case exitItemNotFound:
			return fmt.Errorf("%s: %w", name, ErrNotFound)
		case exitAuthFailed, exitUserCanceled, exitInteractionNotAllowed:
			return fmt.Errorf("%s: %w: %w", name, ErrDenied, err)
		}
	}
	return fmt.Errorf("keychain %s: %w", name, err)
}

// securityCLI is the production backend. go-keyring passes the value to
// `security -i` on stdin, so it never appears in a command line.
type securityCLI struct{}

func (securityCLI) Get(service, name string) (string, error) { return keyring.Get(service, name) }

func (securityCLI) Set(service, name, value string) error { return keyring.Set(service, name, value) }

func (securityCLI) Delete(service, name string) error { return keyring.Delete(service, name) }

// List exists here because go-keyring cannot enumerate accounts. Without -d,
// dump-keychain reads attributes only and never decrypts a value, so it
// cannot trigger the access prompt that reading a secret can.
func (securityCLI) List(service string) ([]string, error) {
	out, err := exec.Command(securityPath, "dump-keychain").Output()
	if err != nil {
		var xerr *exec.ExitError
		if errors.As(err, &xerr) && len(xerr.Stderr) > 0 {
			return nil, fmt.Errorf("security dump-keychain: %w: %s", err, bytes.TrimSpace(xerr.Stderr))
		}
		return nil, fmt.Errorf("security dump-keychain: %w", err)
	}
	return parseDump(out, service), nil
}

// parseDump returns the accounts of the generic password items of service in
// `security dump-keychain` output, where each item starts with a class line.
func parseDump(out []byte, service string) []string {
	var names []string
	var genp, hasSvce, hasAcct bool
	var svce, acct string
	flush := func() {
		if genp && hasSvce && hasAcct && svce == service && acct != "" {
			names = append(names, acct)
		}
		genp, hasSvce, hasAcct = false, false, false
	}
	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "class: "); ok {
			flush()
			genp = v == `"genp"`
		} else if v, ok := strings.CutPrefix(line, `"svce"<blob>=`); ok {
			svce, hasSvce = blobValue(v)
		} else if v, ok := strings.CutPrefix(line, `"acct"<blob>=`); ok {
			acct, hasAcct = blobValue(v)
		}
	}
	flush()
	return names
}

// blobValue decodes one printed blob attribute. security(1) prints a blob
// whose bytes are all printable ASCII other than '\' as "text", unescaped;
// anything else gets 0xHEX first, followed by an escaped rendering that is
// lossy, so the hex form wins when present. <NULL> means the attribute is unset.
func blobValue(v string) (string, bool) {
	if h, ok := strings.CutPrefix(v, "0x"); ok {
		h, _, _ = strings.Cut(h, " ")
		b, err := hex.DecodeString(h)
		return string(b), err == nil
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1], true
	}
	return "", false
}
