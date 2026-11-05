package op

import (
	"io"
	"net/http"
)

// Sink is where a streaming op writes its body: the push model radosgw uses
// in send_response_data, so tail reads go to the socket in order and the
// driver recycles its buffers after each Write returns instead of a reader
// chain holding them. A Write that returns has therefore finished with its
// bytes: it has copied or sent them and keeps no reference. net/http's
// HTTP/1.x response writer does; its HTTP/2 one can return before the frame
// that holds the slice is sent, so a server that speaks HTTP/2 needs a copy in
// the sink first (the frontend serves HTTP/1.x alone).
type Sink interface {
	// WriteHeader sends the status and headers; it is called once, before the first Write.
	WriteHeader(status int, h http.Header)
	io.Writer
	Flush() error
}
