package gomysql

import (
	"errors"
	"strconv"
	"testing"
)

// FuzzParseInt checks parseInt64 and parseUint64 against strconv: the same
// value, and an error for the same inputs. strconv also accepts "+" before
// unsigned numbers and "_" separators with base 0, neither of which is used
// here.
func FuzzParseInt(f *testing.F) {
	for _, s := range []string{"0", "-1", "+5", "9223372036854775807", "-9223372036854775808",
		"9223372036854775808", "18446744073709551615", "18446744073709551616", "", "-", "1a", "007"} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		want, wantErr := strconv.ParseInt(string(b), 10, 64)

		got, err := parseInt64(b)
		if (err != nil) != (wantErr != nil) || err == nil && got != want {
			t.Errorf("parseInt64(%q) = %d, %v; strconv: %d, %v", b, got, err, want, wantErr)
		}

		if errors.Is(wantErr, strconv.ErrRange) && !errors.Is(err, errScanRange) {
			t.Errorf("parseInt64(%q): %v, want a range error", b, err)
		}

		if len(b) > 0 && b[0] == '+' {
			return
		}

		wantU, wantErr := strconv.ParseUint(string(b), 10, 64)

		gotU, err := parseUint64(b)
		if (err != nil) != (wantErr != nil) || err == nil && gotU != wantU {
			t.Errorf("parseUint64(%q) = %d, %v; strconv: %d, %v", b, gotU, err, wantU, wantErr)
		}
	})
}
