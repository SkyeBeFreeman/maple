package keychain

import (
	"errors"
	"fmt"
	"sort"

	gokeychain "github.com/keybase/go-keychain"
)

// New returns a Store backed by generic password items in the user's default
// keychain, with service as the item service and the secret name as account.
func New(service string) Store {
	return &darwinStore{service: service}
}

type darwinStore struct {
	service string
}

func (d *darwinStore) query(name string) gokeychain.Item {
	q := gokeychain.NewItem()
	q.SetSecClass(gokeychain.SecClassGenericPassword)
	q.SetService(d.service)
	q.SetAccount(name)
	return q
}

func (d *darwinStore) Get(name string) (string, error) {
	if name == "" {
		return "", errEmptyName
	}
	q := d.query(name)
	q.SetMatchLimit(gokeychain.MatchLimitOne)
	q.SetReturnData(true)
	res, err := gokeychain.QueryItem(q)
	if err != nil {
		return "", mapErr(name, err)
	}
	// QueryItem reports a missing item as an empty result, not an error.
	if len(res) == 0 {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return string(res[0].Data), nil
}

func (d *darwinStore) Set(name, value string) error {
	if name == "" {
		return errEmptyName
	}
	// Update in place first: delete+add would reset the item's ACL and make
	// macOS prompt again for apps the user already allowed.
	upd := gokeychain.NewItem()
	upd.SetData([]byte(value))
	err := gokeychain.UpdateItem(d.query(name), upd)
	if !errors.Is(err, gokeychain.ErrorItemNotFound) {
		return mapErr(name, err)
	}
	item := d.query(name)
	item.SetLabel(d.service + ": " + name)
	item.SetData([]byte(value))
	item.SetAccessible(gokeychain.AccessibleWhenUnlocked)
	item.SetSynchronizable(gokeychain.SynchronizableNo)
	return mapErr(name, gokeychain.AddItem(item))
}

func (d *darwinStore) Delete(name string) error {
	if name == "" {
		return errEmptyName
	}
	return mapErr(name, gokeychain.DeleteItem(d.query(name)))
}

func (d *darwinStore) List() ([]string, error) {
	accounts, err := gokeychain.GetGenericPasswordAccounts(d.service)
	if err != nil {
		return nil, mapErr(d.service, err)
	}
	sort.Strings(accounts)
	return accounts, nil
}

func mapErr(name string, err error) error {
	if err == nil {
		return nil
	}
	var kerr gokeychain.Error
	if errors.As(err, &kerr) {
		switch kerr {
		case gokeychain.ErrorItemNotFound:
			return fmt.Errorf("%s: %w", name, ErrNotFound)
		case gokeychain.ErrorAuthFailed, gokeychain.ErrorInteractionNotAllowed, gokeychain.ErrorUserCanceled:
			return fmt.Errorf("%s: %w: %w", name, ErrDenied, err)
		}
	}
	return fmt.Errorf("keychain %s: %w", name, err)
}
