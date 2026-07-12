package main

import (
	"crypto/x509"
	"testing"
)

// TestCARootsAreLinkedIn guards main.go's blank import of the CA bundle,
// the same way cmd/server does — and it matters more here, because this
// binary does not talk to Firestore yet. An import that nothing needs today
// is an import someone removes today, and the failure lands on whoever
// writes the append logic, as a TLS error that has nothing to do with them.
//
// x509.SetFallbackRoots may only be called once; the second call panics. So
// a panic here means the bundle's init already ran — the roots are in the
// binary — and no panic means the import is gone.
func TestCARootsAreLinkedIn(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no fallback CA roots in this binary: cmd/importer must blank-import " +
				"golang.org/x/crypto/x509roots/fallback, or TLS to Firestore fails " +
				`with "certificate signed by unknown authority" in the scratch image`)
		}
	}()

	x509.SetFallbackRoots(x509.NewCertPool())
}
