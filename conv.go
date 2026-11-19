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
