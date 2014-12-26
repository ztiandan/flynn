package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/julienschmidt/httprouter"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/sse"
)

func serveHTTP(host *Host, attach *attachHandler, sh *shutdown.Handler) error {
	l, err := net.Listen("tcp", ":1113")
	if err != nil {
		return err
	}
	sh.BeforeExit(func() { l.Close() })

	r := httprouter.New()
	r.POST("/attach", attach)
	r.GET("/host/jobs", hostMiddleware(host, listJobs))
	r.GET("/host/jobs/:id", hostMiddleware(host, getJob))
	r.DELETE("/host/jobs/:id", hostMiddleware(host, stopJob))
	go http.Serve(l, r)

	return nil
}

type Host struct {
	state   *State
	backend Backend
}

func (h *Host) StopJob(id string) error {
	job := h.state.GetJob(id)
	if job == nil {
		return errors.New("host: unknown job")
	}
	switch job.Status {
	case host.StatusStarting:
		h.state.SetForceStop(id)
		return nil
	case host.StatusRunning:
		return h.backend.Stop(id)
	default:
		return errors.New("host: job is already stopped")
	}
}

func (h *Host) streamEvents(id string, w http.ResponseWriter) error {
	ch := h.state.AddListener(id)
	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		h.state.RemoveListener(id, ch)
	}()
	enc := json.NewEncoder(sse.NewSSEWriter(w))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	for data := range ch {
		enc.Encode(data) // TODO: check failure
		w.(http.Flusher).Flush()
	}
	return nil
}

type HostHandle func(*Host, http.ResponseWriter, *http.Request, httprouter.Params)

// Helper function for wrapping a ClusterHandle into a httprouter.Handles
func hostMiddleware(host *Host, handle HostHandle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		handle(host, w, r, ps)
	}
}

func listJobs(h *Host, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	rh := httphelper.NewReponseHelper(w)
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := h.streamEvents("all", w); err != nil {
			rh.Error(err)
		}
		return
	}
	res := h.state.Get()

	rh.JSON(200, res)
}

func getJob(h *Host, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	rh := httphelper.NewReponseHelper(w)
	id := ps.ByName("id")

	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		if err := h.streamEvents(id, w); err != nil {
			rh.Error(err)
		}
		return
	}
	job := h.state.GetJob(id)
	rh.JSON(200, job)
}

func stopJob(h *Host, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	rh := httphelper.NewReponseHelper(w)
	id := ps.ByName("id")
	if err := h.StopJob(id); err != nil {
		rh.Error(err)
		return
	}
	rh.WriteHeader(200)
}
