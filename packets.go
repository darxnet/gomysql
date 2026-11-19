package gomysql

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"syscall"
)

// https://dev.mysql.com/doc/internals/en/compression.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_compression.html

func readNext(r *Conn, buf []byte) ([]byte, error) {
	var total, received int
	var err error

	for total < len(buf) && err == nil {
		received, err = r.reader.Read(buf[total:])
		total += received
	}

	return buf, err
}

func (conn *Conn) readNext(buf []byte) ([]byte, error) {
	if !conn.isCompress {
		return readNext(conn, buf)
	}

	if conn.bufferCompressedDataLen >= len(buf) {
		buf = conn.bufferCompressed.b[:len(buf)]
		conn.bufferCompressed.b = conn.bufferCompressed.b[len(buf):]
		conn.bufferCompressedDataLen -= len(buf)
		return buf, nil
	}

	off := conn.bufferCompressedDataLen

	bufRead, err := readNext(conn, conn.bufferCompressed.GetNCopy(headerCompressSize + off)[off:])
	if err != nil {
		return nil, err
	}

	payloadLen := int(bufRead[0]) | int(bufRead[1])<<8 | int(bufRead[2])<<16
	payloadLenUncompressed := int(bufRead[4]) | int(bufRead[5])<<8 | int(bufRead[6])<<16

	if bufRead[3] != conn.sequenceCompressed {
		return nil, errPacketSequenceCompressedMismatch(conn.sequenceCompressed, bufRead[3])
	}
	conn.sequenceCompressed++

	if payloadLenUncompressed == 0 {
		bufRead, err = readNext(conn, conn.bufferCompressed.GetNCopy(payloadLen + off)[off:])
		if err != nil {
			return nil, err
		}
	} else {
		// zlib reader must implement zlib.Resetter by doc
		if _, ok := conn.readerCompressed.(zlib.Resetter); !ok {
			conn.readerCompressed, err = zlib.NewReaderDict(conn.reader, nil)
		} else {
			err = conn.readerCompressed.(zlib.Resetter).Reset(conn.reader, nil)
		}
		if err != nil {
			return nil, err
		}
		// zlib decode
		bufRead = conn.bufferCompressed.GetNCopy(payloadLenUncompressed + off)[off:]
		n, err := conn.readerCompressed.Read(bufRead)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n != payloadLenUncompressed {
			return nil, malformed("compressed packet shorter than its header says")
		}
	}

	conn.bufferCompressedDataLen += len(bufRead)

	return conn.readNext(buf)
}

// sequenceMismatch reads the payload of a packet with an unexpected
// sequence id: an ERR packet sent by the server on its own, such as 4031
// ER_CLIENT_INTERACTION_TIMEOUT after wait_timeout (MySQL 8.0.24+, seq 0),
// is returned as *Error rather than as ErrOutOfSync. Either way the
// connection is unusable.
func (conn *Conn) sequenceMismatch(received byte, payloadLen int) error {
	errSeq := errPacketSequenceMismatch(conn.sequence, received)

	if payloadLen == 0 || payloadLen >= maxPayloadSize {
		return errSeq
	}

	buf, err := conn.readNext(conn.buffer.GetN(payloadLen))
	if err != nil || buf[0] != iERR {
		return errSeq
	}

	return conn.processERRPacket(buf)
}

// https://dev.mysql.com/doc/internals/en/mysql-packet.html
func (conn *Conn) readPacket() ([]byte, error) {
	var prevB []byte

	for {
		// process header

		buf, err := conn.readNext(conn.buffer.GetN(headerSize))
		if err != nil {
			return nil, err
		}

		payloadLen := int(buf[0]) | int(buf[1])<<8 | int(buf[2])<<16
		if buf[3] != conn.sequence {
			return nil, conn.sequenceMismatch(buf[3], payloadLen)
		}
		conn.sequence++

		// process payload

		if payloadLen == 0 {
			if prevB == nil {
				return nil, errPacketPayloadEmpty
			}

			return prevB, nil
		}

		buf, err = conn.readNext(conn.buffer.GetN(payloadLen))
		if err != nil {
			return nil, err
		}

		if payloadLen < maxPayloadSize {
			if prevB == nil {
				return buf, nil // return initial buf, avoid copy
			}

			return append(prevB, buf...), nil
		}

		prevB = append(prevB, buf...)
	}
}

// appendUint24 appends n as a 3-byte little-endian integer, the size field
// of packet headers.
func appendUint24(dst []byte, n int) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(n)) //nolint:gosec // G115: packet sizes are below 1<<24

	return dst[:len(dst)-1]
}

// writePacket writes buf as packets of at most maxPayloadSize bytes; a
// payload of exactly maxPayloadSize is followed by an empty packet.
//
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_packets.html
func writePacket(conn *Conn, writer io.Writer, buf []byte) error {
	for {
		payloadLen := min(len(buf), maxPayloadSize)

		header := append(appendUint24(make([]byte, 0, headerSize), payloadLen), conn.sequence)

		n, err := writer.Write(header)
		if err != nil {
			return err
		}

		if n != headerSize {
			return errConnWroteFewerBytesThanSent
		}

		n, err = writer.Write(buf[:payloadLen])
		if err != nil {
			return err
		}

		if n != payloadLen {
			return errConnWroteFewerBytesThanSent
		}

		conn.sequence++

		if payloadLen < maxPayloadSize {
			return nil
		}

		buf = buf[payloadLen:]
	}
}

func (conn *Conn) writePacket(buf []byte) (err error) {
	if !conn.isCompress {
		err = writePacket(conn, conn.writer, buf)
		if err != nil {
			return err
		}
		err = conn.writer.Flush()
		return err
	}

	var n int

	// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_compression_packet.html
	packetLen := headerSize + len(buf)
	if packetLen < writerMinCompressSize {
		// write without compressing
		header := append(appendUint24(nil, packetLen), conn.sequenceCompressed, 0x00, 0x00, 0x00)

		n, err = conn.writer.Write(header)
		if err != nil {
			return err
		}
		if n != headerCompressSize {
			err = errConnWroteFewerBytesThanSent
			return err
		}
		// write buf
		err = writePacket(conn, conn.writer, buf)
		if err != nil {
			return err
		}
		err = conn.writer.Flush()
	} else {
		// prepare buffer
		var bufCompressed = bytes.NewBuffer(nil)
		conn.writerCompressed.Reset(bufCompressed)
		// write packets
		err = writePacket(conn, conn.writerCompressed, buf)
		if err != nil {
			return err
		}
		err = conn.writerCompressed.Close()
		if err != nil {
			return err
		}
		// write compressed packet header
		packetLen = len(buf) + (len(buf)/maxPayloadSize+1)*headerSize
		payloadLenCompressed := bufCompressed.Len()
		header := appendUint24(append(appendUint24(nil, payloadLenCompressed), conn.sequenceCompressed), packetLen)

		n, err = conn.writer.Write(header)
		if err != nil {
			return err
		}
		if n != headerCompressSize {
			err = errConnWroteFewerBytesThanSent
			return err
		}
		// write bufCompressed
		n, err = conn.writer.Write(bufCompressed.Bytes())
		if err != nil {
			return err
		}
		if n != payloadLenCompressed {
			err = errConnWroteFewerBytesThanSent
			return err
		}
		err = conn.writer.Flush()
	}

	conn.sequenceCompressed++

	return err
}

func (conn *Conn) writePacketBuf(buf []byte) (err error) {
	conn.sequence = 0
	conn.sequenceCompressed = 0
	err = conn.writePacket(buf)
	if err != nil {
		// Hidden reconnect on a broken pipe; it loses transactions and
		// prepared statements and goes away in ROADMAP phase 4.
		if errors.Is(err, syscall.EPIPE) {
			if errConn := conn.Conn(); errConn != nil {
				return errConn
			}

			return conn.writePacketBuf(buf)
		}
	}

	return err
}

func (conn *Conn) writePacketCom(com byte) (err error) {
	err = conn.writePacketBuf([]byte{com})
	return err
}

func (conn *Conn) writePacketComStr(com byte, s string) (err error) {
	err = conn.writePacketBuf(append([]byte{com}, s...))
	return err
}

func (conn *Conn) writePacketComBytes(com byte, buf []byte) (err error) {
	err = conn.writePacketBuf(append([]byte{com}, buf...))
	return err
}

// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_eof_packet.html
func (conn *Conn) readUntilEOF() (err error) {
	for {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		switch buf[0] {
		case iEOF: // EOF, or OK with the EOF header under CLIENT_DEPRECATE_EOF
			conn.processEOFPacket(buf)
			return nil
		case iERR:
			return conn.processERRPacket(buf)
		case iOK:
			// do nothing, just read unprocessed packets
		default:
			return errUnexpectedPacketIdentity(buf[0])
		}
	}
}

// readMoreResults drains all result sets that follow the current one.
func (conn *Conn) readMoreResults() error {
	for conn.statusFlags&serverStatusMoreResultsExists != 0 {
		buf, err := conn.readPacket()
		if err != nil {
			return err
		}

		switch buf[0] {
		case iOK:
			conn.processOKPacket(buf)
		case iERR:
			return conn.processERRPacket(buf)
		case iLocalInfile:
			return errLocalInfile
		default:
			columns, _ := lenDecInt(buf)
			for range columns {
				if _, err = conn.readPacket(); err != nil {
					return err
				}
			}

			if conn.capFlags&clientDeprecateEOF == 0 {
				buf, err = conn.readPacket()
				if err != nil {
					return err
				}
				if buf[0] != iEOF {
					return errExpectedEOF
				}
				conn.processEOFPacket(buf)
			}

			if err := conn.readUntilEOF(); err != nil {
				return err
			}
		}
	}

	return nil
}

// https://dev.mysql.com/doc/internals/en/packet-OK_Packet.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_ok_packet.html
func (conn *Conn) processOKPacket(buf []byte) {
	var pos, posN int

	pos += 1 // header: 0x00, or 0xfe under CLIENT_DEPRECATE_EOF

	// affected rows
	conn.rowsAffected, posN = lenDecInt(buf[pos:])
	pos += posN

	// last insert-id
	conn.lastInsertId, posN = lenDecInt(buf[pos:])
	pos += posN

	if len(buf) >= pos+4 {
		conn.statusFlags = uint16(buf[pos]) | uint16(buf[pos+1])<<8
	}
}

// https://dev.mysql.com/doc/internals/en/packet-ERR_Packet.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_err_packet.html
// https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_expired_passwords.html not implemented
func (conn *Conn) processERRPacket(buf []byte) error {
	if len(buf) < 3 {
		return malformed("ERR packet")
	}

	e := &Error{Number: binary.LittleEndian.Uint16(buf[1:])}
	buf = buf[3:]

	// sql_state_marker '#' and the 5-byte SQL state; absent in errors sent
	// before the handshake, such as 1129 "Host blocked".
	if len(buf) >= 6 && buf[0] == '#' {
		copy(e.SQLState[:], buf[1:6])
		buf = buf[6:]
	}

	e.Message = string(buf)

	return e
}

func (conn *Conn) processEOFPacket(buf []byte) {
	if len(buf) >= 5 {
		conn.statusFlags = uint16(buf[3]) | uint16(buf[4])<<8
	}
}
