package store

import (
	"context"
	"strings"
	"testing"

	"github.com/leozh0u/blundernet/internal/testdb"
)

// Recovery is checked against a real Postgres because the interesting parts
// are the transaction in RecoverWithCode and the NULL handling on accounts
// that predate the column, neither of which a fake would exercise.
func testUsers(t *testing.T) (*Users, context.Context) {
	t.Helper()
	ctx := context.Background()
	archive, err := NewArchive(ctx, testdb.URL(t, "store_test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archive.Close)
	if _, err := archive.Pool().Exec(ctx, "TRUNCATE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	return NewUsers(archive.Pool()), ctx
}

// A recovery code has to survive being written down and typed back in, so the
// comparison ignores case and the grouping dashes.
func TestRecoveryCodeIsForgivingAboutFormatting(t *testing.T) {
	users, ctx := testUsers(t)

	u, err := users.Create(ctx, "typist", "correcthorse1")
	if err != nil {
		t.Fatal(err)
	}
	code, err := users.SetRecoveryCode(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code, "-") {
		t.Fatalf("expected a grouped code, got %q", code)
	}

	// Lower case, dashes stripped, and a stray space in the middle.
	mangled := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	got, next, err := users.RecoverWithCode(ctx, "typist", mangled, "brandnewpass1")
	if err != nil {
		t.Fatalf("mangled but correct code rejected: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("recovered the wrong account")
	}
	if next == "" || next == code {
		t.Fatal("spending a code must return a different one")
	}
	if _, err := users.Authenticate(ctx, "typist", "brandnewpass1"); err != nil {
		t.Fatalf("new password does not work: %v", err)
	}
}

// Spending a code has to retire it. Otherwise a code seen once in a
// screenshot keeps working forever.
func TestUsedRecoveryCodeStopsWorking(t *testing.T) {
	users, ctx := testUsers(t)

	u, _ := users.Create(ctx, "spender", "correcthorse1")
	code, err := users.SetRecoveryCode(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := users.RecoverWithCode(ctx, "spender", code, "secondpassword1"); err != nil {
		t.Fatal(err)
	}
	_, _, err = users.RecoverWithCode(ctx, "spender", code, "thirdpassword1")
	if err != ErrBadRecovery {
		t.Fatalf("a spent code still worked: %v", err)
	}
	// And the password from the first recovery is still the live one.
	if _, err := users.Authenticate(ctx, "spender", "secondpassword1"); err != nil {
		t.Fatal("the replay changed the password anyway")
	}
}

// An unknown user, a wrong code, and an account with no code must be
// indistinguishable, or the endpoint enumerates accounts.
func TestRecoveryFailuresAreIndistinguishable(t *testing.T) {
	users, ctx := testUsers(t)

	noCode, _ := users.Create(ctx, "nocode", "correcthorse1")
	_ = noCode
	withCode, _ := users.Create(ctx, "hascode", "correcthorse1")
	if _, err := users.SetRecoveryCode(ctx, withCode.ID); err != nil {
		t.Fatal(err)
	}

	cases := []struct{ name, user, code string }{
		{"unknown user", "ghost", "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE"},
		{"no code set", "nocode", "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE"},
		{"wrong code", "hascode", "ABCDE-ABCDE-ABCDE-ABCDE-ABCDE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := users.RecoverWithCode(ctx, c.user, c.code, "whatever12345")
			if err != ErrBadRecovery {
				t.Fatalf("want ErrBadRecovery, got %v", err)
			}
		})
	}
}

// Guests have no credentials, so there is nothing to recover and no code to
// issue. Without this the guest row would get a code it can never use.
func TestGuestsGetNoRecoveryCode(t *testing.T) {
	users, ctx := testUsers(t)

	g, err := users.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.SetRecoveryCode(ctx, g.ID); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for a guest, got %v", err)
	}
}

// Two codes from the same account must differ. A generator that repeats would
// mean one leaked code opens every account it ever issued for.
func TestRecoveryCodesAreDistinct(t *testing.T) {
	users, ctx := testUsers(t)

	u, _ := users.Create(ctx, "roller", "correcthorse1")
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		code, err := users.SetRecoveryCode(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if seen[code] {
			t.Fatalf("generator repeated a code: %q", code)
		}
		seen[code] = true
	}
}
