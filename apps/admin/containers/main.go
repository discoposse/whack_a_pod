// Copyright 2017 Google Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package main is a Kubernetes API proxy. It exposes a smaller surface of the
// API and limits operations to specifically selected labels, and deployments
package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"time"
)

var (
	client              httpClient
	pool                *x509.CertPool
	token               = ""
	errItemNotExist     = fmt.Errorf("Item does not exist")
	errItemAlreadyExist = fmt.Errorf("Item already exists")
)

const (
	root             = "https://kubernetes"
	selector         = "app=api"
	defaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultCertPath  = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

func main() {
	log.Printf("starting whack a pod admin api")
	var err error

	token, err = tokenFromDisk(defaultTokenPath)
	if err != nil {
		log.Printf("could not get token from file system")
	}

	certs, err := certsFromDisk(defaultCertPath)
	if err != nil {
		log.Printf("could not get token from file system")
	}

	// This allows me to use a scratch Dockerfile as described here :
	// https://medium.com/@kelseyhightower/optimizing-docker-images-for-static-binaries-b5696e26eb07
	// But instead of using the Authoritative Certs from a linux install, I'm
	// using the certs mounted from the Kuberntes server itself.  Since all
	// this client does is talk to the Kubernetes server, this should always be
	// up to date.
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(certs)
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		Addr:         ":8080",
		Handler:      handler(),
	}

	srv.ListenAndServe()
}

func handler() http.Handler {

	r := http.NewServeMux()
	r.HandleFunc("/", health)
	r.HandleFunc("/healthz", health)
	r.HandleFunc("/admin/healthz", health)
	r.HandleFunc("/healthz/", health)
	r.HandleFunc("/admin/healthz/", health)

	r.HandleFunc("/admin/k8s/pods/get", handleAPI(handlePods))
	r.HandleFunc("/admin/k8s/nodes/get", handleAPI(handleNodes))
	r.HandleFunc("/admin/k8s/pod/delete", handleAPI(handlePodDelete))
	r.HandleFunc("/admin/k8s/pods/delete", handleAPI(handlePodsDelete))
	r.HandleFunc("/admin/k8s/node/drain", handleAPI(handleNodeDrain))
	r.HandleFunc("/admin/k8s/node/uncordon", handleAPI(handleNodeUncordon))
	r.HandleFunc("/admin/k8s/deployment/delete", handleAPI(handleDeploymentDelete))
	r.HandleFunc("/admin/k8s/deployment/create", handleAPI(handleDeploymentCreate))
	r.HandleFunc("/admin/k8s/pods/get/", handleAPI(handlePods))
	r.HandleFunc("/admin/k8s/nodes/get/", handleAPI(handleNodes))
	r.HandleFunc("/admin/k8s/pod/delete/", handleAPI(handlePodDelete))
	r.HandleFunc("/admin/k8s/pods/delete/", handleAPI(handlePodsDelete))
	r.HandleFunc("/admin/k8s/node/drain/", handleAPI(handleNodeDrain))
	r.HandleFunc("/admin/k8s/node/uncordon/", handleAPI(handleNodeUncordon))
	r.HandleFunc("/admin/k8s/deployment/delete/", handleAPI(handleDeploymentDelete))
	r.HandleFunc("/admin/k8s/deployment/create/", handleAPI(handleDeploymentCreate))
	return r
}

func health(w http.ResponseWriter, r *http.Request) {
	r.Close = true
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

type apiHandler func(http.ResponseWriter, *http.Request) ([]byte, error)

func handleAPI(h apiHandler) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
}

func (h apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Close = true
	w.Header().Add("Access-Control-Allow-Origin", "*")
	b, err := h(w, r)
	status := http.StatusOK
	if err != nil {
		status = http.StatusInternalServerError
		if err == errItemNotExist {
			status = http.StatusAccepted
		}

		if err == errItemAlreadyExist {
			status = http.StatusAccepted
		}

		sendJSON(w, fmt.Sprintf("{\"error\":\"%v\"}", err), status)
		log.Printf("%s %d %s", r.Method, status, r.URL)
		log.Printf("Error %v", err)
		return
	}
	sendJSON(w, string(b), status)
	log.Printf("%s %d %s", r.Method, status, r.URL)
}

func handlePods(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	b, err := listPods()
	if err != nil {
		return nil, fmt.Errorf("could not retrieve k8s api: %v", err)
	}

	return b, nil
}

func handlePodDelete(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	b, err := deletePod(r.FormValue("pod"))
	if err != nil {
		if err == errItemNotExist {
			return nil, errItemNotExist
		}

		return nil, fmt.Errorf("could not delete k8s object: %v", err)
	}

	return b, nil
}

func handlePodsDelete(w http.ResponseWriter, r *http.Request) ([]byte, error) {

	b, err := deletePods("")
	if err != nil && err != errItemNotExist {
		return nil, fmt.Errorf("could not delete k8s pods: %v", err)
	}
	return b, nil
}

func handleDeploymentCreate(w http.ResponseWriter, r *http.Request) ([]byte, error) {

	b, err := createDeployment()
	if err != nil {
		return nil, fmt.Errorf("could not create k8s deployment: %v", err)
	}

	return b, nil
}

func handleDeploymentDelete(w http.ResponseWriter, r *http.Request) ([]byte, error) {

	_, err := deleteDeployment("api-deployment")
	if err != nil && err != errItemNotExist {
		return nil, fmt.Errorf("could not delete k8s deployment: %v", err)
	}

	_, err = deleteReplicaSet()
	if err != nil && err != errItemNotExist {
		return nil, fmt.Errorf("could not delete k8s replica set: %v", err)
	}

	return handlePodsDelete(w, r)
}

func handleNodes(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	b, err := listNodes()
	if err != nil {
		return nil, fmt.Errorf("could not get list of k8s nodes: %v", err)
	}

	return b, nil
}

func handleNodeDrain(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	nodename := r.FormValue("node")

	b, err := toggleNode(nodename, true)
	if err != nil && err != errItemNotExist {
		return nil, fmt.Errorf("could not retrieve k8s node info: %v", err)
	}

	_, err = deletePods(nodename)
	if err != nil {
		return nil, fmt.Errorf("could not remove all pods on node: %v", err)
	}

	return b, nil
}

func handleNodeUncordon(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	nodename := r.FormValue("node")

	b, err := toggleNode(nodename, false)
	if err != nil && err != errItemNotExist {
		return nil, fmt.Errorf("could not retrieve k8s node info: %v", err)
	}

	return b, nil
}

func sendJSON(w http.ResponseWriter, content string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, content)
}
