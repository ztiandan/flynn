package httphelper

import (
	"encoding/json"
	"log"
	"net/http"
)

// shamelessly taken from martini-contrib/render/render.go
const (
	ContentType = "Content-Type"
	ContentJSON = "application/json"
)

type ResponseHelper interface {
	Error(error)
	JSON(int, interface{})
	WriteHeader(int)
}

type responseHelper struct {
	http.ResponseWriter
}

func NewReponseHelper(w http.ResponseWriter) ResponseHelper {
	helper := &responseHelper{w}
	return ResponseHelper(helper)
}

func (r *responseHelper) Head(status int) {
	r.WriteHeader(status)
}

func (r *responseHelper) JSON(status int, v interface{}) {
	var result []byte
	var err error
	result, err = json.Marshal(v)
	if err != nil {
		http.Error(r, err.Error(), 500)
		return
	}

	// json rendered fine, write out the result
	r.Header().Set(ContentType, ContentJSON)
	r.WriteHeader(status)
	r.Write(result)
}

func (r *responseHelper) Error(err error) {
	switch err.(type) {
	case *json.SyntaxError, *json.UnmarshalTypeError:
		r.JSON(400, "The provided JSON input is invalid")
	default:
		log.Println(err)
		r.JSON(500, struct{}{})
	}
}
