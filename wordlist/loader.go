package wordlist

import (
	"fmt"
	"os"
)

type PathRef struct {
	Start int
	End   int
}

type Wordlist struct {
	Data  []byte
	Paths []PathRef
}

func Load(filename string) (*Wordlist, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read wordlist: %w", err)
	}

	paths := make([]PathRef, 0, 10000)

	start := 0

	for i, b := range data {

		if b != '\n' {
			continue
		}

		end := i

		if end > start && data[end-1] == '\r' {
			end--
		}

		if end > start {
			paths = append(paths, PathRef{
				Start: start,
				End:   end,
			})
		}

		start = i + 1
	}

	if start < len(data) {

		end := len(data)

		if end > start && data[end-1] == '\r' {
			end--
		}

		if end > start {
			paths = append(paths, PathRef{
				Start: start,
				End:   end,
			})
		}
	}

	return &Wordlist{
		Data:  data,
		Paths: paths,
	}, nil
}

func (w *Wordlist) Get(index int) []byte {
	ref := w.Paths[index]

	return w.Data[ref.Start:ref.End]
}

func (w *Wordlist) Len() int {
	return len(w.Paths)
}