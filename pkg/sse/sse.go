package sse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

type SSEWriter interface {
	Write([]byte) (int, error)
	Flush()
}

func NewSSEWriter(w io.Writer) SSEWriter {
	return &Writer{Writer: w}
}

type Writer struct {
	io.Writer
	sync.Mutex
}

func (w *Writer) Write(p []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	for _, line := range bytes.Split([]byte("\n"), p) {
		data := fmt.Sprintf("data: %s\n", line)
		if _, err := w.Writer.Write([]byte(data)); err != nil {
			return 0, err
		}
	}
	// add a terminating newline
	_, err := w.Writer.Write([]byte("\n"))
	return len(p), err
}

func (w *Writer) Flush() {
	if fw, ok := w.Writer.(http.Flusher); ok {
		fw.Flush()
	}
}

type Reader struct {
	*bufio.Reader
}

func (r *Reader) Read() ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		if bytes.HasPrefix(line, []byte("data: ")) {
			data := bytes.TrimSuffix(bytes.TrimPrefix(line, []byte("data: ")), []byte("\n"))
			return data, nil
		}
	}
}

type Decoder struct {
	*Reader
}

func NewDecoder(r *bufio.Reader) *Decoder {
	return &Decoder{&Reader{r}}
}

// Decode finds the next "data" field and decodes it into v
func (dec *Decoder) Decode(v interface{}) error {
	data, err := dec.Reader.Read()
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
