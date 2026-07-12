package main

import (
	"crypto/x509"
	"testing"
)

// TestCARootsAreLinkedIn guards main.go's blank import of the CA bundle.
//
// That import is the whole trust store of a binary that ships in a scratch
// image, and it is exactly the kind of line that gets deleted while tidying
// up: nothing in the code reads from it, no compile fails without it, and
// on a developer's machine — which has its own root certificates — the app
// keeps working. It breaks in the one place that has no root certificates,
// which is production.
//
// The test binary compiles this package, so it runs main.go's imports. If
// the bundle's init installed the fallback roots, a second
// x509.SetFallbackRoots must panic (documented: it may only be called
// once), and that panic is the proof the roots are in the binary.
func TestCARootsAreLinkedIn(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no fallback CA roots in this binary: cmd/server must blank-import " +
				"golang.org/x/crypto/x509roots/fallback, or TLS to Firestore fails " +
				`with "certificate signed by unknown authority" in the scratch image`)
		}
	}()

	// Panics iff the roots were already installed. The pool is a throwaway:
	// in the branch where this does not panic, the test has already failed.
	x509.SetFallbackRoots(x509.NewCertPool())
}
