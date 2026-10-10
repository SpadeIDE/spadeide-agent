package hostauth

import "testing"

func TestPasswordDisabled(t *testing.T) {
	for _, hash := range []string{"", "*", "!", "!!", "!$y$j9T$abc"} {
		if !passwordDisabled(hash) {
			t.Errorf("disabled hash %q not detected", hash)
		}
	}
	if passwordDisabled("$y$j9T$abc$def") {
		t.Fatal("live yescrypt hash treated as locked")
	}
}

func TestParseShadowHash(t *testing.T) {
	hash, ok := parseShadowHash("alice:$y$j9T$abc$def:19000:0:99999:7:::", "alice")
	if !ok || hash != "$y$j9T$abc$def" {
		t.Fatalf("got hash=%q ok=%v", hash, ok)
	}
	if _, ok := parseShadowHash("bob:$6$xxx:19000:0:99999:7:::", "alice"); ok {
		t.Fatal("matched the wrong user")
	}
}

func TestPasswdIDFromHashChangesWithPassword(t *testing.T) {
	a, err := passwdIDFromHash("alice", "$y$j9T$old")
	if err != nil {
		t.Fatal(err)
	}
	b, err := passwdIDFromHash("alice", "$y$j9T$new")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("password change must change passwd id")
	}
	c, err := passwdIDFromHash("alice", "$y$j9T$old")
	if err != nil {
		t.Fatal(err)
	}
	if a != c {
		t.Fatal("same hash must be stable")
	}
	if _, err := passwdIDFromHash("alice", "!"); err != ErrAccountLocked {
		t.Fatalf("locked: %v", err)
	}
}

func TestLookupAccountRefusesRoot(t *testing.T) {
	if _, err := LookupAccount("root"); err != ErrRefusedRoot {
		t.Fatalf("root: got %v, want ErrRefusedRoot", err)
	}
	if _, err := LookupAccount(""); err != ErrNoAccount && err != ErrRefusedRoot {
		t.Fatalf("empty: got %v", err)
	}
}

func TestLookupAccountCurrentUser(t *testing.T) {
	u := currentNonRoot(t)
	if u == "" {
		t.Skip("no non-root user")
	}
	acct, err := LookupAccount(u)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Name == "root" || acct.UID == 0 {
		t.Fatalf("must not bind root: %+v", acct)
	}
	if acct.Home == "" || acct.Home == "/" {
		t.Fatalf("missing home: %+v", acct)
	}
}

func currentNonRoot(t *testing.T) string {
	t.Helper()
	u := osUserName()
	if u == "" || u == "root" {
		return ""
	}
	return u
}
