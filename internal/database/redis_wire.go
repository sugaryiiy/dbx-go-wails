package database

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
)

// Bound RESP frames before go-redis sees a length prefix and allocates a bulk
// value/array. COUNT is merely a hint and a single stored value can be huge.
// Returning a clear error preserves pagination correctness instead of dropping
// items from a scan reply. This connection is request-owned and then closed.
type redisBoundedConn struct {
	net.Conn
	reader  *bufio.Reader
	pending *bytes.Reader
}

func newRedisBoundedConn(conn net.Conn) net.Conn {
	return &redisBoundedConn{Conn: conn, reader: bufio.NewReader(conn)}
}
func (c *redisBoundedConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.pending == nil || c.pending.Len() == 0 {
		var frame bytes.Buffer
		remaining := 16 << 20
		if err := c.frame(&frame, &remaining, 0); err != nil {
			return 0, err
		}
		c.pending = bytes.NewReader(frame.Bytes())
	}
	return c.pending.Read(p)
}
func (c *redisBoundedConn) frame(dst *bytes.Buffer, remaining *int, depth int) error {
	if depth > 16 {
		return errors.New("Redis reply nesting exceeds the supported limit")
	}
	line, err := c.reader.ReadSlice('\n')
	if err != nil {
		return err
	}
	if len(line) < 3 || line[len(line)-2] != '\r' {
		return errors.New("invalid Redis response framing")
	}
	if len(line) > *remaining {
		return errors.New("Redis response exceeds the 16 MiB limit")
	}
	*remaining -= len(line)
	switch line[0] {
	case '+', '-', ':':
		dst.Write(line)
		return nil
	case '$', '*':
		n, err := strconv.ParseInt(string(line[1:len(line)-2]), 10, 64)
		if err != nil || n < -1 {
			return errors.New("invalid Redis response length")
		}
		if line[0] == '$' {
			if n > int64(*remaining)-2 {
				return errors.New("Redis value exceeds the 16 MiB response limit")
			}
			dst.Write(line)
			if n < 0 {
				return nil
			}
			copied, err := io.CopyN(dst, c.reader, n+2)
			*remaining -= int(copied)
			if err == nil && !bytes.HasSuffix(dst.Bytes(), []byte("\r\n")) {
				return errors.New("invalid Redis bulk terminator")
			}
			return err
		}
		if n > 100000 {
			return errors.New("Redis collection response exceeds 100000 items; use a narrower scan")
		}
		dst.Write(line)
		for i := int64(0); i < n; i++ {
			if err := c.frame(dst, remaining, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported Redis response protocol; RESP2 is required")
	}
}
