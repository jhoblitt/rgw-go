package op

import (
	"io"
	"net/http"
)

// Sink is where a streaming op writes its body: the push model radosgw uses
// in send_response_data, so tail reads go to the socket in order and the
// driver recycles its buffers after each Write returns instead of a reader
// chain holding them.
type Sink interface {
	// WriteHeader sends the status and headers; it is called once, before the first Write.
	WriteHeader(status int, h http.Header)
	io.Writer
	Flush() error
}
