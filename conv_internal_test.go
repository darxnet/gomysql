package gomysql

import (
	"math"
	"strconv"
	"testing"
)

func TestParseInt(t *testing.T) {
	t.Parallel()

	for _, n := range []int64{0, 1, -1, 42, -42, math.MaxInt64, math.MinInt64} {
		if got := parseInt[int64]([]byte(strconv.FormatInt(n, 10))); got != n {
			t.Errorf("parseInt[int64](%d) = %d", n, got)
		}
	}

	for _, n := range []int8{0, -1, math.MaxInt8, math.MinInt8} {
		if got := parseInt[int8]([]byte(strconv.Itoa(int(n)))); got != n {
			t.Errorf("parseInt[int8](%d) = %d", n, got)
		}
	}
}

func TestParseUint(t *testing.T) {
	t.Parallel()

	for _, n := range []uint64{0, 1, 42, math.MaxUint32, math.MaxUint64} {
		if got := parseUint[uint64]([]byte(strconv.FormatUint(n, 10))); got != n {
			t.Errorf("parseUint[uint64](%d) = %d", n, got)
		}
	}
}
