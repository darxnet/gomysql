package gomysql

type signed interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

type unsigned interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// parseUint parses the decimal digits of a text-protocol integer. The
// server sends valid digits, so there is no validation; overflow wraps.
func parseUint[T unsigned](bs []byte) T {
	var n T
	for _, b := range bs {
		n = n*10 + T(b-'0')
	}

	return n
}

// parseInt is parseUint with an optional leading '-'.
func parseInt[T signed](bs []byte) T {
	neg := len(bs) > 0 && bs[0] == '-'
	if neg {
		bs = bs[1:]
	}

	var n T
	for _, b := range bs {
		n = n*10 + T(b-'0')
	}

	if neg {
		return -n
	}

	return n
}

func b2string(b byte) string {
	var bs [3]byte // 3 because "255" is max
	var i byte = 0
	if b >= 100 { // get hundreds
		bs[0] = '0' + b/100
		b %= 100
		i++
	}
	if b >= 10 {
		bs[1] = '0' + b/10
		b %= 10
		i++
	}
	bs[2] = '0' + b
	i++
	return string(bs[3-i:])
}

func b2hex(b byte) string {
	const digits string = "0123456789abcdef"
	bs := [4]byte{'0', 'x', '0', '0'}
	if b >= 16 {
		bs[2] = digits[b/16]
		b %= 16
	}
	bs[3] = digits[b]
	return string(bs[:])
}
