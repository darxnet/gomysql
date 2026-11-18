package gomysql

import "testing"

func TestLenDecInt(t *testing.T) {
	t.Parallel()

	for _, want := range []uint64{
		0,
		251,
		252,
		253,
		1<<16 - 1,
		1 << 16,
		1<<24 - 1,
		1 << 24,
	} {
		encoded := appendLenEncInt(nil, want)
		got, size := lenDecInt(encoded)
		if got != want || size != len(encoded) {
			t.Fatalf("lenDecInt(%x) = (%d, %d), want (%d, %d)", encoded, got, size, want, len(encoded))
		}
	}
}
