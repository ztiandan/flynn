package volumeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/julienschmidt/httprouter"
	"github.com/flynn/flynn/host/volume"
	"github.com/flynn/flynn/host/volume/zfs"
	"github.com/flynn/flynn/pkg/httphelper"
)

type HttpAPI struct {
	vman *volume.Manager
}

func NewHttpAPI(vman *volume.Manager) *HttpAPI {
	return &HttpAPI{vman: vman}
}

func RegisterRoutes(api *HttpAPI, r *httprouter.Router) {
	r.POST("/volume/provider", api.CreateProvider)
	r.POST("/volume/provider/:provider_id/newVolume", api.Create)
	r.PUT("/volume/x/:id/snapshot", api.Snapshot)
	//r.GET("/volume/x/:id/inspect", api.Inspect) // very TODO
}

func (api *HttpAPI) CreateProvider(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		httphelper.Error(w, err)
		return
	}

	var provider volume.Provider
	pspec := &volume.ProviderSpec{}
	if err = json.Unmarshal(data, &pspec); err != nil {
		httphelper.Error(w, err)
		return
	}
	switch pspec.Kind {
	case "zfs":
		if parentDataset, ok := pspec.Metadata["parent_dataset"]; ok {
			var err error // shadowing prevention for `provider` -.-
			if provider, err = zfs.NewProvider(parentDataset); err != nil {
				httphelper.JSON(w, 500, err)
				return
			}
		} else {
			httphelper.JSON(w, 400, errors.New("host: zfs volume provider requires a 'parent_dataset' parameter"))
			return
		}
	case "":
		httphelper.JSON(w, 400, errors.New("host: volume provider kind must not be blank"))
		return
	default:
		httphelper.JSON(w, 400, fmt.Errorf("host: volume provider kind '%s' is not known"))
		return
	}

	// REVIEW/DESIGN: do we let clients pick the IDs here, or do we pick and return it?
	if err := api.vman.AddProvider("todo-ID", provider); err != nil {
		switch err {
		case volume.ProviderAlreadyExists:
			httphelper.JSON(w, 400, err)
			return
		default:
			httphelper.JSON(w, 500, err)
			return
		}
	}

	ret := struct{}{}
	httphelper.JSON(w, 200, ret)
}

func (api *HttpAPI) Create(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	providerID := ps.ByName("provider_id")

	vol, err := api.vman.NewVolumeFromProvider(providerID)
	if err == volume.NoSuchProvider {
		// TODO: produce a message that hints this is an id-not-found rather than youre-barking-up-the-wrong-api?
		httphelper.JSON(w, 404, err)
		return
	}

	httphelper.JSON(w, 200, vol.ID()) // TODO: perhaps 'inspect' return structure, after we pin that down?
}

func (api *HttpAPI) Snapshot(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	// TODO
}
