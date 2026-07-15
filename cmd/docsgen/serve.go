package main

import (
	"fmt"
	"net/http"
	"time"
)

// serveSite serves root over HTTP for local preview, replacing the
// `python3 -m http.server` habit with something the repo already builds.
//
// root is the site root (docs/), not the docs output dir, so the docs sit at
// /docs/ exactly as they do on the deployed site and every relative href
// resolves the same way here as in production.
func serveSite(addr, root string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.FileServer(http.Dir(root)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Printf("docsgen: serving %s at http://%s/docs/ (ctrl-c to stop)\n", root, addr)
	return srv.ListenAndServe()
}
