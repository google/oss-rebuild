package api

// A message is a request/response type, used in api.Stub
type Message interface {
	Validate() error
}
