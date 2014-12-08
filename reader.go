// The `fwd` package provides a buffered reader that can
// seek forward an arbitrary number of bytes. The Peek() and
// Skip() methods are useful for manipulating the contents of a
// byte-stream in place, as well as a shim to allow the use of
// `[]byte`-oriented methods with io.Readers. Additionally,
// if the underlying reader implements io.Seeker, then
// Skip() uses that to skip forward as well.
//
// (This package was
// originally written to improve decoding speed in
// github.com/philhofer/msgp/msgp.)
package fwd

import (
	"io"
)

const (
	// DefaultReaderSize is the default size of the read buffer
	DefaultReaderSize = 2048

	// minimum read buffer; straight from bufio
	minReaderSize = 16
)

// NewReader returns a new *Reader that reads from 'r'
func NewReader(r io.Reader) *Reader {
	return NewReaderSize(r, DefaultReaderSize)
}

// NewReaderSize returns a new *Reader that
// reads from 'r' and has a buffer size 'n'
func NewReaderSize(r io.Reader, n int) *Reader {
	rd := &Reader{
		r:    r,
		data: make([]byte, 0, max(minReaderSize, n)),
	}
	if s, ok := r.(io.Seeker); ok {
		rd.rs = s
	}
	return rd
}

// Reader is a buffered look-ahead reader
type Reader struct {
	r io.Reader // underlying reader

	// data[n:len(data)] is buffered data; data[len(data):cap(data)] is free buffer space
	data  []byte // data
	n     int    // read offset
	state error  // last read error

	// if the reader past to NewReader was
	// also an io.Seeker, this is non-nil
	rs io.Seeker
}

// Reset resets the underlying reader
// and the read buffer.
func (r *Reader) Reset(rd io.Reader) {
	r.r = rd
	r.data = r.data[0:0]
	r.n = 0
	r.state = nil
	r.rs = nil
	if s, ok := rd.(io.Seeker); ok {
		r.rs = s
	}
}

// more() does one read on the underlying reader
func (r *Reader) more() {
	// move data backwards so that
	// the read offset is 0; this way
	// we can supply the maximum number of
	// bytes to the reader
	if r.n != 0 {
		r.data = r.data[:copy(r.data[0:], r.data[r.n:])]
		r.n = 0
	}
	var a int
	a, r.state = r.r.Read(r.data[len(r.data):cap(r.data)])
	if a == 0 && r.state == nil {
		r.state = io.ErrNoProgress
		return
	}
	r.data = r.data[:len(r.data)+a]
}

// pop error
func (r *Reader) err() (e error) {
	e, r.state = r.state, nil
	return
}

// pop error; EOF -> io.ErrUnexpectedEOF
func (r *Reader) noEOF() (e error) {
	e, r.state = r.state, nil
	if e == io.EOF {
		e = io.ErrUnexpectedEOF
	}
	return
}

// buffered bytes
func (r *Reader) buffered() int { return len(r.data) - r.n }

// Buffered returns the number of bytes currently in the buffer
func (r *Reader) Buffered() int { return len(r.data) - r.n }

// BufferSize returns the total size of the buffer
func (r *Reader) BufferSize() int { return cap(r.data) }

// Peek returns the next 'n' buffered bytes,
// reading from the underlying reader if necessary.
// It will only return a slice shorter than 'n' bytes
// if it also returns an error. Peek does not advance
// the reader. EOF errors are *not* returned as
// io.ErrUnexpectedEOF.
func (r *Reader) Peek(n int) ([]byte, error) {
	// in the degenerate case,
	// we may need to realloc
	// (the caller asked for more
	// bytes than the size of the buffer)
	if cap(r.data) < n {
		old := r.data[r.n:]
		r.data = make([]byte, n+r.buffered())
		r.data = r.data[:copy(r.data, old)]
	}

	// keep filling until
	// we hit an error or
	// read enough bytes
	for r.buffered() < n && r.state == nil {
		r.more()
	}

	// we must have hit an error
	if r.buffered() < n {
		return r.data[r.n:], r.err()
	}

	return r.data[r.n : r.n+n], nil
}

// Skip moves the reader forward 'n' bytes.
// Returns the number of bytes skipped and any
// errors encountered. It is analagous to Seek(n, 1).
// If the underlying reader implements io.Seeker, then
// that method will be used to skip forward.
//
// If the reader encounters
// an EOF before skipping 'n' bytes, it
// returns io.ErrUnexpectedEOF. If the
// underlying reader implements io.Seeker, then
// those rules apply instead. (Many implementations
// will not return io.EOF until the next call
// to Read.)
func (r *Reader) Skip(n int) (int, error) {

	// fast path
	if r.buffered() >= n {
		r.n += n
		return n, nil
	}

	// use seeker implementation
	// if we can
	if r.rs != nil {
		return r.skipSeek(n)
	}

	// loop on filling
	// and then erasing
	o := n
	for r.buffered() < n && r.state == nil {
		r.more()
		// we can skip forward
		// up to r.buffered() bytes
		step := min(r.buffered(), n)
		r.n += step
		n -= step
	}
	return o - n, r.noEOF()
}

// Next returns the next 'n' bytes in the stream.
// If the returned slice has a length less than 'n',
// an error will also be returned.
// Unlike Peek, Next advances the reader position.
// The returned bytes point to the same
// data as the buffer, so the slice is
// only valid until the next reader method call.
// An EOF is considered an unexpected error.
func (r *Reader) Next(n int) ([]byte, error) {

	// in case the buffer is too small
	if cap(r.data) < n {
		old := r.data[r.n:]
		r.data = make([]byte, n+r.buffered())
		r.data = r.data[:copy(r.data, old)]
	}

	// fill at least 'n' bytes
	for r.buffered() < n && r.state == nil {
		r.more()
	}

	if r.buffered() < n {
		return r.data[r.n:], r.noEOF()
	}
	out := r.data[r.n : r.n+n]
	r.n += n
	return out, nil
}

// skipSeek uses the io.Seeker to seek forward.
// only call this function when n > r.buffered()
func (r *Reader) skipSeek(n int) (int, error) {
	o := n
	// first, clear buffer
	n -= r.buffered()
	r.n = 0
	r.data = r.data[:0]
	_, err := r.rs.Seek(int64(n), 1)

	// the best assumption
	// we can make here is
	// that we either skipped
	// everything or nothing...
	if err != nil {
		return 0, err
	}
	return o, nil
}

// Read implements `io.Reader`
func (r *Reader) Read(b []byte) (int, error) {
	if len(b) <= r.buffered() {
		x := copy(b, r.data[r.n:])
		r.n += x
		return x, nil
	}
	r.more()
	if r.buffered() > 0 {
		x := copy(b, r.data[r.n:])
		r.n += x
		return x, nil
	}

	// io.Reader is supposed to return
	// 0 read bytes on error
	return 0, r.err()
}

// ReadFull attempts to read len(b) bytes into
// 'b'. It returns the number of bytes read into
// 'b', and an error if it does not return len(b).
func (r *Reader) ReadFull(b []byte) (int, error) {
	var x int
	l := len(b)
	for x < l {
		if r.buffered() == 0 {
			r.more()
		}
		c := copy(b[x:], r.data[r.n:])
		x += c
		r.n += c
		if r.state != nil {
			return x, r.noEOF()
		}
	}
	return x, nil
}

// ReadByte implements `io.ByteReader`
func (r *Reader) ReadByte() (byte, error) {
	for r.buffered() < 1 && r.state == nil {
		r.more()
	}
	if r.buffered() < 1 {
		return 0, r.err()
	}
	b := r.data[r.n]
	r.n++
	return b, nil
}

// WriteTo implements `io.WriterTo`
func (r *Reader) WriteTo(w io.Writer) (int64, error) {
	var (
		i   int64
		ii  int
		err error
	)
	// first, clear buffer
	if r.buffered() > 0 {
		ii, err = w.Write(r.data[r.n:])
		i += int64(ii)
		if err != nil {
			return i, err
		}
		r.data = r.data[0:0]
		r.n = 0
	}
	for r.state == nil {
		// here we just do
		// 1:1 reads and writes
		r.more()
		if r.buffered() > 0 {
			ii, err = w.Write(r.data)
			i += int64(ii)
			if err != nil {
				return i, err
			}
			r.data = r.data[0:0]
			r.n = 0
		}
	}
	if r.state != io.EOF {
		return i, r.err()
	}
	return i, nil
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a int, b int) int {
	if a < b {
		return b
	}
	return a
}
