package store

import (
	"strings"
	"testing"
)

func TestPasswordVerifies(t *testing.T) {
	hash, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword("correct horse battery staple", hash) {
		t.Error("the right password did not verify")
	}
	if verifyPassword("correct horse battery stapl", hash) {
		t.Error("a wrong password verified")
	}
}

func TestSamePasswordHashesDifferently(t *testing.T) {
	// Distinct salts, so identical passwords do not produce identical hashes
	// and a stolen table cannot be scanned for users who share one.
	a, err := hashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical, so the salt is not random")
	}
	if !verifyPassword("hunter2", a) || !verifyPassword("hunter2", b) {
		t.Error("both should still verify")
	}
}

func TestHashCarriesItsParameters(t *testing.T) {
	// The point of the encoded form: the cost parameters travel with the hash,
	// so raising them later does not invalidate existing passwords.
	hash, err := hashPassword("whatever")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$argon2id$", "m=65536", "t=1", "p=4"} {
		if !strings.Contains(hash, want) {
			t.Errorf("hash %q is missing %q", hash, want)
		}
	}
}

func TestMalformedHashesAreRejected(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=65536,t=1,p=4$onlyfourfields",
		"$bcrypt$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=1$m=65536,t=1,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$garbage$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=1,p=4$!!!notbase64$aGFzaA",
	} {
		if verifyPassword("whatever", bad) {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}
