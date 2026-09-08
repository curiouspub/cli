package preflight

import (
	"errors"
	"io"
)

// errTooLarge is what readCapped returns for a file bigger than the
// limit it was handed.
//
// A SENTINEL RATHER THAN A SENTENCE, because the size a reader refuses
// is a property of the question being asked and not of reading. Each
// caller states its own limit and says what exceeding it means, and one
// of them turns this into a message a person reads — so the words belong
// there, beside the number they are about.
var errTooLarge = errors.New("larger than this check reads")

// readCapped reads at most limit bytes of name and refuses anything
// bigger, rather than reading an accidental blob into memory.
//
// THE SIZE IS CHECKED TWICE AND THE SECOND CHECK IS THE ONE THAT COUNTS.
// A stat answers cheaply and settles the ordinary case, but it is a
// claim about the file as it was a moment ago; the read itself is
// therefore bounded at limit+1 bytes and the length checked again. A
// file that grew between the two calls is refused rather than silently
// truncated, and a filesystem that reports a size of zero for a file
// that has contents cannot slip past by answering the first question
// wrongly.
func readCapped(fsys FS, name string, limit int64) ([]byte, error) {
	info, err := fsys.Stat(name)
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, errTooLarge
	}

	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}
