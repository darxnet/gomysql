package gomysql

type buffer struct {
	b []byte
}

func (b *buffer) GetN(n int) []byte {
	if n > cap(b.b) {
		b.b = make([]byte, n)
	}

	return b.b[:n]
}

func (b *buffer) GetNCopy(n int) []byte {
	if n > cap(b.b) {
		tmp := make([]byte, n)
		copy(tmp, b.b)
		b.b = tmp
	}

	return b.b[:n]
}
