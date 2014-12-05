package sampi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/julienschmidt/httprouter"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/httphelper"
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

func (s *Cluster) ListHosts(ret *map[string]host.Host) error {
	*ret = s.state.Get()
	return nil
}

func (s *Cluster) AddJobs(req *host.AddJobsReq, res *host.AddJobsRes) error {
	s.state.Begin()
	*res = host.AddJobsRes{}
	for host, jobs := range req.HostJobs {
		if err := s.state.AddJobs(host, jobs); err != nil {
			s.state.Rollback()
			return err
		}
	}
	res.State = s.state.Commit()

	for host, jobs := range req.HostJobs {
		for _, job := range jobs {
			s.state.SendJob(host, job)
		}
	}

	return nil
}

// Host Service methods

func (s *Cluster) RegisterHost(hostID *string, h *host.Host, ch chan *host.Job, done chan bool) error {
	*hostID = h.ID
	id := *hostID
	if id == "" {
		return errors.New("sampi: host id must not be blank")
	}

	s.state.Begin()

	if s.state.HostExists(id) {
		s.state.Rollback()
		return errors.New("sampi: host exists")
	}

	jobs := make(chan *host.Job)
	s.state.AddHost(h, jobs)
	s.state.Commit()
	go s.state.sendEvent(id, "add")

	var err error
	for job := range jobs {
		ch <- job
	}

	s.state.Begin()
	s.state.RemoveHost(id)
	s.state.Commit()
	go s.state.sendEvent(id, "remove")
	if err == io.EOF {
		err = nil
	}
	return err
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
	ret := make(map[string]host.Host)
	err := c.ListHosts(&ret)
	if err != nil {
		rh.Error(err)
		return
	}
	w.WriteHeader(200)
	rh.JSON(200, ret)
}

func registerHost(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	var err error
	var data []byte
	var hostID string

	rh := httphelper.NewReponseHelper(w)
	//hID := ps.ByName("id")
	h := &host.Host{}

	_, err = r.Body.Read(data)
	if err != nil {
		rh.Error(err)
		return
	}
	err = json.Unmarshal(data, &h)
	if err != nil {
		rh.Error(err)
		return
	}

	ch := make(chan *host.Job)
	done := make(chan bool)
	err = c.RegisterHost(&hostID, h, ch, done)
	if err != nil {
		rh.Error(err)
		return
	}

	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		done <- true
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
	var err error
	var data []byte

	rh := httphelper.NewReponseHelper(w)
	//hostID := ps.ByName("host_id")
	req := host.AddJobsReq{}
	res := host.AddJobsRes{}

	_, err = r.Body.Read(data)
	if err != nil {
		rh.Error(err)
		return
	}
	err = json.Unmarshal(data, &req)
	if err != nil {
		rh.Error(err)
		return
	}
	err = c.AddJobs(&req, &res)
	if err != nil {
		rh.Error(err)
		return
	}
	rh.JSON(200, res)
}

func removeJob(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	hostID := ps.ByName("host_id")
	jobIDs := []string{ps.ByName("job_id")}
	err := c.RemoveJobs(hostID, jobIDs)
	if err != nil {
		httphelper.NewReponseHelper(w).Error(err)
		return
	}
	w.WriteHeader(200)
}

func streamHostEvents(c *Cluster, w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ch := make(chan host.HostEvent)
	done := make(chan bool)
	err := c.StreamHostEvents(ch, done)
	if err != nil {
		httphelper.NewReponseHelper(w).Error(err)
		return
	}
	go func() {
		<-w.(http.CloseNotifier).CloseNotify()
		done <- true
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

func NewHTTPClusterRouter(cluster *Cluster) *httprouter.Router {
	r := httprouter.New()
	r.GET("/cluster/hosts", clusterMiddleware(cluster, listHosts))
	r.PUT("/cluster/hosts/:id", clusterMiddleware(cluster, registerHost))
	r.POST("/cluster/hosts/:host_id/jobs", clusterMiddleware(cluster, addJobs))
	r.DELETE("/cluster/hosts/:host_id/jobs/:job_id", clusterMiddleware(cluster, removeJob))
	r.GET("/cluster/events", clusterMiddleware(cluster, streamHostEvents))
	return r
}
