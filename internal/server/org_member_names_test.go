package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type orgNamesDB struct{}
type orgNamesRow struct{}

func (orgNamesDB) QueryRow(context.Context, string, ...any) pgx.Row { return orgNamesRow{} }
func (orgNamesDB) Query(context.Context, string, ...any) (store.Rows, error) {
	return nil, errors.New("unexpected query")
}
func (orgNamesDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected write")
}
func (orgNamesRow) Scan(dest ...any) error {
	*dest[0].(*string) = "alice"
	*dest[1].(*string) = "alice-account"
	*dest[2].(*string) = "private@example.com"
	*dest[3].(*string) = `{"display_name":"Alex Owner","private_setting":"never expose this"}`
	*dest[4].(*time.Time) = time.Now()
	return nil
}

func TestOrganizationMembersExposeDisplayNamesNotPrivateProfileFields(t *testing.T) {
	s := newTestServer()
	s.Accounts = store.NewUserStore(orgNamesDB{})
	mem := s.Store.(*store.MemStore)
	org, err := mem.CreateOrganization(t.Context(), "Named team", "named-team", "alice")
	if err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodGet, "/orgs/"+org.ID, loginAs(t, s, "alice"), nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Alex Owner"`) {
		t.Fatalf("missing display name: %d %s", rec.Code, rec.Body.String())
	}
	for _, secret := range []string{"private@example.com", "private_setting", "never expose this"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("organization detail leaked %q", secret)
		}
	}
	rec = doJSON(t, s, http.MethodGet, "/orgs/"+org.ID, loginAs(t, s, "outsider"), nil)
	if rec.Code != 403 || strings.Contains(rec.Body.String(), "Alex Owner") {
		t.Fatal("non-member can read organization directory")
	}
}
