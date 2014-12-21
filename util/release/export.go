package main

import (
	"log"
	"net/url"
	"os/exec"

	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/flynn/go-docopt"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/vbatts/docker-utils/registry"
	"github.com/flynn/flynn/pkg/cliutil"
)

func export(args *docopt.Args) {
	var manifest []string
	if err := cliutil.DecodeJSONArg(args.String["<manifest>"], &manifest); err != nil {
		log.Fatal(err)
	}

	reg := registry.Registry{Path: args.String["<dir>"]}
	if err := reg.Init(); err != nil {
		log.Fatal(err)
	}

	names := make([]string, len(manifest))
	for i, s := range manifest {
		u, err := url.Parse(s)
		if err != nil {
			log.Fatal(err)
		}
		names[i] = u.Query().Get("name")
	}

	cmd := exec.Command("docker", append([]string{"save"}, names...)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	if err := registry.ExtractTar(&reg, out); err != nil {
		log.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		log.Fatal(err)
	}
}
