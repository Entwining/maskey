package activity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// maxLine bounds a line read; records are far shorter.
const maxLine = 64 * 1024

// Line is a record read back, with the bytes it was read from.
type Line struct {
	Record
	At  time.Time
	Raw []byte // without its line ending
}

// Skipped counts the lines a read could not use: lines that are no record
// of this format or too long to be one, and records of a later format.
type Skipped struct {
	Unreadable int
	Newer      int
}

// Read returns matching records at or after since, newest first. It opens
// month files from that UTC month onward; a missing directory is empty.
func (l *Log) Read(since time.Time, match func(Record) bool) ([]Line, Skipped, error) {
	var lines []Line
	var skipped Skipped
	entries, err := os.ReadDir(l.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, skipped, err
	}
	first := time.Date(since.UTC().Year(), since.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		if month, ok := monthOf(entry.Name()); !ok || month.Before(first) {
			continue
		}
		file, err := os.Open(filepath.Join(l.dir, entry.Name()))
		if err != nil {
			return nil, skipped, err
		}
		// A line longer than maxLine is no record.
		reader := bufio.NewReaderSize(file, maxLine)
		for {
			raw, err := reader.ReadSlice('\n')
			if errors.Is(err, bufio.ErrBufferFull) {
				skipped.Unreadable++
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = reader.ReadSlice('\n')
				}
				continue
			}
			if errors.Is(err, io.EOF) && len(raw) == 0 {
				break
			}
			if err != nil && !errors.Is(err, io.EOF) {
				file.Close()
				return nil, skipped, err
			}
			var r Record
			if json.Unmarshal(raw, &r) != nil || r.V < 1 {
				skipped.Unreadable++
				continue
			}
			if r.V > Version {
				skipped.Newer++
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, r.Time)
			if err != nil {
				skipped.Unreadable++
				continue
			}
			if at.Before(since) || !match(r) {
				continue
			}
			lines = append(lines, Line{r, at, bytes.Clone(bytes.TrimSuffix(raw, []byte("\n")))})
		}
		file.Close()
	}
	// Processes append concurrently, so a file's order is nearly but not
	// exactly the records' order, and files come in name order.
	slices.SortStableFunc(lines, func(a, b Line) int { return b.At.Compare(a.At) })
	return lines, skipped, nil
}
