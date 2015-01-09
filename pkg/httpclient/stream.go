package httpclient

import (
	"bufio"
	"io"
	"net/http"

	"github.com/flynn/flynn/pkg/sse"
	"github.com/flynn/flynn/pkg/stream"

	"github.com/flynn/flynn/host/types" // get rid of
)

/*
	Stream manufactors a `pkg.Stream`, starts a worker pumping events out of decoding, and returns that.

	The return values from `httpclient.RawReq` are probably a useful starting point for the 'res' parameter.

	Closing the returned `Stream` shuts down the worker.
*/
func Stream(res *http.Response, output chan<- interface{}) stream.Stream {
	stream := stream.New()
	go func() {
		defer func() {
			close(output) // FIXME: this would have to be emulated by a method that wraps 'output' to disguise the chan type
			res.Body.Close()
		}()

		r := bufio.NewReader(res.Body)
		dec := sse.NewDecoder(r)
		for {
			event := &host.Event{} // FIXME: this would have to be emulated by a factory method
			if err := dec.Decode(event); err != nil {
				if err != io.EOF {
					stream.Error = err
				}
				break
			}
			output <- event // FIXME: this would have to be emulated by a method that wraps 'output' to disguise the chan type
		}
	}()
	return stream
}
