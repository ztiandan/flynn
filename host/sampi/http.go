package sampi

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/julienschmidt/httprouter"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/sse"
)

// Cluster
type Cluster struct {
	state *State
}

func NewCluster(state *State) *Cluster {
	return &Cluster{state}
}

// Scheduler Methods

func (s *Cluster) ListHosts() ([]host.Host, error) {
	hostMap := s.state.Get()
	hostSlice := make([]host.Host, 0, len(hostMap))
	for _, h := range hostMap {
		hostSlice = append(hostSlice, h)
	}
	return hostSlice, nil
}

func (s *Cluster) AddJobs(req map[string][]*host.Job) (map[string]host.Host, error) {
	s.state.Begin()
	for host, jobs := range req {
		if err := s.state.AddJobs(host, jobs); err != nil {
			s.state.Rollback()
			return nil, err
		}
	}
	res := s.state.Commit()

	for host, jobs := range req {
		for _, job := range jobs {
			s.state.SendJob(host, job)
		}
	}

	return res, nil
}

// Host Service methods

func (s *Cluster) RegisterHost(h *host.Host, ch chan *host.Job, done chan bool) error {
	if h.ID == "" {
		return errors.New("sampi: host id must not be blank")
	}

	s.state.Begin()

	if s.state.HostExists(h.ID) {
		s.state.Rollback()
		return errors.New("sampi: host exists")
	}

	jobs := make(chan *host.Job)
	s.state.AddHost(h, jobs)
	s.state.Commit()
	go s.state.sendEvent(h.ID, "add")

outer:
	for {
		select {
		case job := <-jobs:
			ch <- job
		case <-done:
			break outer
		}
	}
	close(jobs)

	s.state.Begin()
	s.state.RemoveHost(h.ID)
	s.state.Commit()
	go s.state.sendEvent(h.ID, "remove")
	return nil
}

func (s *Cluster) RemoveJobs(hostID string, jobIDs []string) error {
	s.state.Begin()
	s.state.RemoveJobs(hostID, jobIDs...)
	s.state.Commit()
	return nil
}

func (s *Cluster) StreamHostEvents(ch chan host.HostEvent, done chan bool) error {
	s.state.AddListener(ch)
	go func() {
		<-done
		go func() {
			// drain to prevent deadlock while removing the listener
			for range ch {
			}
		}()
		s.state.RemoveListener(ch)
		close(ch)
	}()
	return nil
}

// HTTP Route Handles
func listHosts(c *Cluster, w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	rh := httphelper.NewReponseHelper(w)
	ret, err := c.ListHosts()
	if err != nil {
		rh.Error(err)
		return
	}
	w.WriteHeader(200)
	rh.JSON(200, ret)
}

func registerHost(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	rh := httphelper.NewReponseHelper(w)

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rh.Error(err)
		return
	}

	h := &host.Host{}
	if err = json.Unmarshal(data, &h); err != nil {
		rh.Error(err)
		return
	}

	ch := make(chan *host.Job)
	done := make(chan bool)
	if err = c.RegisterHost(h, ch, done); err != nil {
		rh.Error(err)
		return
	}

	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		close(done)
	}()
	wr := sse.NewSSEWriter(w)
	enc := json.NewEncoder(wr)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(200)
	wr.Flush()
	for data := range ch {
		enc.Encode(data)
		wr.Flush()
	}
}

func addJobs(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	rh := httphelper.NewReponseHelper(w)

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rh.Error(err)
		return
	}

	var req map[string][]*host.Job
	if err := json.Unmarshal(data, &req); err != nil {
		rh.Error(err)
		return
	}
	res, err := c.AddJobs(req)
	if err != nil {
		rh.Error(err)
		return
	}
	rh.JSON(200, res)
}

func removeJob(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	hostID := ps.ByName("host_id")
	jobIDs := []string{ps.ByName("job_id")}
	if err := c.RemoveJobs(hostID, jobIDs); err != nil {
		httphelper.NewReponseHelper(w).Error(err)
		return
	}
	w.WriteHeader(200)
}

func streamHostEvents(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ch := make(chan host.HostEvent)
	done := make(chan bool)
	if err := c.StreamHostEvents(ch, done); err != nil {
		httphelper.NewReponseHelper(w).Error(err)
		return
	}
	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		close(done)
	}()
	wr := sse.NewSSEWriter(w)
	enc := json.NewEncoder(wr)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(200)
	wr.Flush()
	for data := range ch {
		enc.Encode(data)
		wr.Flush()
	}
}

type ClusterHandle func(*Cluster, http.ResponseWriter, *http.Request, httprouter.Params)

// Helper function for wrapping a ClusterHandle into a httprouter.Handles
func clusterMiddleware(cluster *Cluster, handle ClusterHandle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		handle(cluster, w, r, ps)
	}
}

func (cluster *Cluster) ServeHTTP(r *httprouter.Router, sh *shutdown.Handler) error {
	r.GET("/cluster/hosts", clusterMiddleware(cluster, listHosts))
	r.PUT("/cluster/hosts/:id", clusterMiddleware(cluster, registerHost))
	r.POST("/cluster/hosts/:host_id/jobs", clusterMiddleware(cluster, addJobs))
	r.DELETE("/cluster/hosts/:host_id/jobs/:job_id", clusterMiddleware(cluster, removeJob))
	r.GET("/cluster/events", clusterMiddleware(cluster, streamHostEvents))
	return nil
}
