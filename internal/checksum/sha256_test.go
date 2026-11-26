package checksum

import "testing"

func TestSum(t *testing.T) {
	if got := Sum([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("unexpected SHA-256 %s", got)
	}
}
