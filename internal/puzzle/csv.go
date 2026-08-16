package puzzle

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
)

// Reader streams parsed puzzles out of the Lichess CSV. It is the shape the
// bulk loader wants: one row at a time, so the six million row file never
// lands in memory.
//
// A row that will not parse is skipped and counted rather than fatal. The file
// is machine generated and has been clean so far, but a load that dies on row
// five million because of one malformed record is worse than a load that
// finishes and says what it dropped.
type Reader struct {
	csv     *csv.Reader
	filter  func(Puzzle) bool
	limit   int
	cur     Puzzle
	read    int
	skipped int
	err     error
}

// ReaderOption configures a Reader.
type ReaderOption func(*Reader)

// WithFilter keeps only the puzzles the predicate accepts. Rejected rows count
// as skipped.
func WithFilter(f func(Puzzle) bool) ReaderOption {
	return func(r *Reader) { r.filter = f }
}

// WithLimit stops after n accepted puzzles. Zero means no limit. This exists
// so a local run can load a hundred thousand puzzles in seconds instead of
// waiting out the whole file to find out the schema was wrong.
func WithLimit(n int) ReaderOption {
	return func(r *Reader) { r.limit = n }
}

func NewReader(r io.Reader, opts ...ReaderOption) (*Reader, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true // the record is parsed into a Puzzle before the next read
	cr.FieldsPerRecord = -1

	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if err := CheckHeader(head); err != nil {
		return nil, err
	}

	out := &Reader{csv: cr}
	for _, o := range opts {
		o(out)
	}
	return out, nil
}

func (r *Reader) Next() bool {
	for {
		if r.err != nil {
			return false
		}
		if r.limit > 0 && r.read >= r.limit {
			return false
		}
		rec, err := r.csv.Read()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			// A parse error inside the CSV framing is not a bad row, it is a
			// truncated or corrupt file, so it stops the load.
			r.err = err
			return false
		}
		p, err := Parse(rec)
		if err != nil {
			r.skipped++
			continue
		}
		if r.filter != nil && !r.filter(p) {
			r.skipped++
			continue
		}
		r.cur = p
		r.read++
		return true
	}
}

func (r *Reader) Puzzle() Puzzle { return r.cur }

func (r *Reader) Err() error { return r.err }

// Read is how many puzzles were accepted, Skipped how many were dropped as
// unparseable or filtered out.
func (r *Reader) Read() int    { return r.read }
func (r *Reader) Skipped() int { return r.skipped }
