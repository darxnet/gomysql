package gomysql

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// errNoFile is the error of LoadData with a statement that is not LOAD
// DATA LOCAL INFILE; it ran.
var errNoFile = inSyncError{errors.New("gomysql: LoadData: the statement asked for no file")}

// loadDataChunk is the size of the packets of LoadData: below the smallest
// default max_allowed_packet (4 MB in MySQL 5.7).
const loadDataChunk = 1 << 20

// LoadData runs query, a LOAD DATA LOCAL INFILE statement, and sends r as
// its file; the file name in the statement does not matter. Nothing is
// read from the disk. The server must allow it (local_infile=ON, OFF by
// default since MySQL 8.0).
//
// If r fails, the connection is closed, so that the server aborts the
// statement: an InnoDB table gets none of the rows sent before. The error
// wraps the error of r and ErrBadConn.
func (conn *Conn) LoadData(query string, r io.Reader) (*Result, error) {
	return conn.LoadDataContext(context.Background(), query, r)
}

// LoadDataContext is LoadData within ctx.
func (conn *Conn) LoadDataContext(ctx context.Context, query string, r io.Reader) (*Result, error) {
	if err := conn.begin(ctx); err != nil {
		return nil, err
	}

	if err := writeCommand(conn, comQuery, query); err != nil {
		return nil, conn.end(err)
	}

	conn.infile = r

	result, err := conn.readResult()
	if err == nil && conn.infile != nil {
		err = errNoFile
	}

	conn.infile = nil

	return result, conn.end(err)
}

// sendFile answers the server's request for a file (0xfb, the name) with
// the reader of LoadData, in packets of loadDataChunk bytes and an empty
// one. Outside LoadData it sends an empty file and returns errLocalInfile
// once the server answers. Then the response to the statement follows.
func (conn *Conn) sendFile() error {
	r := conn.infile
	if r == nil {
		if err := conn.writePacket(nil); err != nil {
			return err
		}

		if _, err := conn.readResultHeader(); err != nil {
			return err
		}

		return inSyncError{errLocalInfile}
	}

	conn.infile = nil

	buf := make([]byte, headerSize+loadDataChunk)

	for {
		n, err := io.ReadFull(r, buf[headerSize:])
		if n > 0 {
			if werr := conn.writeRaw(buf[:headerSize+n]); werr != nil {
				return werr
			}
		}

		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}

		if err != nil {
			// The server aborts the statement only if the file does not end.
			_ = conn.conn.Close()

			return fmt.Errorf("gomysql: LoadData reader: %w: %w", err, ErrBadConn)
		}
	}

	if err := conn.writePacket(nil); err != nil {
		return err
	}

	_, err := conn.readResultHeader()

	return err
}

// writeRaw sends p, a packet with room for its header at the start, as
// the next packet; it is written without a copy.
func (conn *Conn) writeRaw(p []byte) error {
	n := len(p) - headerSize
	p[0], p[1], p[2] = byte(n), byte(n>>8), byte(n>>16) //nolint:gosec // G115: n < 1<<24
	p[3] = conn.sequence
	conn.sequence++

	if conn.compress != nil {
		return conn.writeCompressed(p)
	}

	_, err := conn.conn.Write(p)

	return err
}
