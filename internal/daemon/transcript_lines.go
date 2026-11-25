package daemon

// Allocation-free helpers for reading transcript lines.
//
// Agent transcripts are dominated by records the harvester never uses:
// Claude Code writes every raw API request body as an "api-request-blob"
// line (hundreds of KB that repeat the conversation), plus progress,
// file-history and queue records. Decoding each of those into a
// map[string]any (once to look for a turn, again to look for token usage)
// was most of the capture pipeline's memory churn. These helpers let the
// tailer reject such lines by their top-level "type" without decoding them,
// and read files line by line without buffering whole files.

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// jsonTopLevelType returns the top-level "type" string of a JSON object line
// without decoding the line. ok is false when the line is not an object or
// has no top-level string "type". Escapes in the value are returned raw.
func jsonTopLevelType(line []byte) (typ string, ok bool) {
	n := len(line)
	i := skipJSONSpace(line, 0)
	if i >= n || line[i] != '{' {
		return "", false
	}
	i++
	for {
		i = skipJSONSpace(line, i)
		if i >= n || line[i] != '"' {
			return "", false // '}' (no more keys) or malformed
		}
		end := jsonStringEnd(line, i)
		if end < 0 {
			return "", false
		}
		key := line[i+1 : end]
		i = skipJSONSpace(line, end+1)
		if i >= n || line[i] != ':' {
			return "", false
		}
		i = skipJSONSpace(line, i+1)
		if i >= n {
			return "", false
		}
		if string(key) == "type" {
			if line[i] != '"' {
				return "", false
			}
			vend := jsonStringEnd(line, i)
			if vend < 0 {
				return "", false
			}
			return string(line[i+1 : vend]), true
		}
		i = skipJSONValue(line, i)
		if i < 0 {
			return "", false
		}
		i = skipJSONSpace(line, i)
		if i >= n || line[i] != ',' {
			return "", false
		}
		i++
	}
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// jsonStringEnd returns the index of the closing quote of the string that
// opens at b[i], or -1.
func jsonStringEnd(b []byte, i int) int {
	for k := i + 1; k < len(b); k++ {
		switch b[k] {
		case '\\':
			k++
		case '"':
			return k
		}
	}
	return -1
}

// skipJSONValue returns the index just past the value starting at b[i], or -1.
func skipJSONValue(b []byte, i int) int {
	switch b[i] {
	case '"':
		end := jsonStringEnd(b, i)
		if end < 0 {
			return -1
		}
		return end + 1
	case '{', '[':
		depth := 0
		for k := i; k < len(b); k++ {
			switch b[k] {
			case '"':
				end := jsonStringEnd(b, k)
				if end < 0 {
					return -1
				}
				k = end
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return k + 1
				}
			}
		}
		return -1
	default:
		for k := i; k < len(b); k++ {
			switch b[k] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return k
			}
		}
		return len(b)
	}
}

// skippableTranscriptLine reports a line that can yield no turn, no token
// usage and no title, judged by its top-level type alone: the noise records
// parseTurnLine drops, except turn_context (it names the model for usage).
func skippableTranscriptLine(line []byte) bool {
	typ, ok := jsonTopLevelType(line)
	if !ok {
		return false
	}
	typ = strings.ToLower(typ)
	return noiseTypes[typ] && typ != "turn_context"
}

// transcriptLineReader yields complete lines ('\n'-terminated) from r without
// holding more than one line in memory. A line longer than max is skipped
// whole (its bytes still count). An unterminated tail is not returned, so a
// torn write is read again once the writer finishes it.
type transcriptLineReader struct {
	r    *bufio.Reader
	max  int
	long []byte // reused for lines longer than the bufio buffer
}

func newTranscriptLineReader(r io.Reader, max int) *transcriptLineReader {
	return &transcriptLineReader{r: bufio.NewReaderSize(r, 64<<10), max: max}
}

// next returns the next complete line without its '\n' (and without a
// trailing '\r'), the number of bytes it occupied including the newline,
// and whether it was skipped as overlong. ok is false at the end of the
// complete lines. The returned slice is only valid until the next call.
func (lr *transcriptLineReader) next() (line []byte, size int, overlong, ok bool) {
	lr.long = lr.long[:0]
	tooLong := false
	for {
		chunk, err := lr.r.ReadSlice('\n')
		size += len(chunk)
		if !tooLong {
			if len(lr.long)+len(chunk) > lr.max+2 { // allow for "\r\n"
				tooLong = true
				lr.long = lr.long[:0]
			} else if err == bufio.ErrBufferFull || len(lr.long) > 0 {
				lr.long = append(lr.long, chunk...)
			}
		}
		switch err {
		case nil:
			if tooLong {
				return nil, size, true, true
			}
			if len(lr.long) > 0 {
				chunk = lr.long
			}
			chunk = bytes.TrimSuffix(chunk[:len(chunk)-1], []byte{'\r'})
			return chunk, size, false, true
		case bufio.ErrBufferFull:
			continue
		default: // io.EOF or a read error: an unterminated tail is not a line
			return nil, 0, false, false
		}
	}
}
