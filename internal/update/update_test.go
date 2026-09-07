package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestSignatureAndFileIntegrity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	p := filepath.Join(t.TempDir(), AssetName)
	_ = os.WriteFile(p, []byte("a release executable"), 0700)
	raw, sig, e := Sign(p, "1.2.3", priv)
	if e != nil {
		t.Fatal(e)
	}
	m, e := Verify(raw, sig, pub)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyFile(p, m); e != nil {
		t.Fatal(e)
	}
	tampered := append([]byte{}, raw...)
	tampered[4] ^= 1
	if _, e = Verify(tampered, sig, pub); e == nil {
		t.Fatal("accepted changed metadata")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, e = Verify(raw, sig, other); e == nil {
		t.Fatal("accepted unknown publisher")
	}
	_ = os.WriteFile(p, []byte("another executable!!"), 0700)
	if e = VerifyFile(p, m); e == nil {
		t.Fatal("accepted changed executable")
	}
}
func TestStableVersionComparison(t *testing.T) {
	cases := []struct {
		next, current string
		want          bool
	}{{"1.10.0", "1.9.0", true}, {"v2.0.0", "1.9.9", true}, {"1.0.0", "1.0.0", false}, {"0.9.9", "1.0.0", false}, {"1.1.0-beta", "1.0.0", false}, {"01.2.3", "1.0.0", false}, {"../../exe", "1.0.0", false}}
	for _, c := range cases {
		if got := Newer(c.next, c.current); got != c.want {
			t.Errorf("Newer(%q,%q)=%v", c.next, c.current, got)
		}
	}
}
