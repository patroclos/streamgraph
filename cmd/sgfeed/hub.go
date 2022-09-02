package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/patroclos/go-conq"
)

func runHub(c conq.Ctx, addr string) {
	http.Handle("/init", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func(e *error) {
			if e != nil {
				log.Println(*e)
			}
		}(&err)
		header := w.Header()
		header.Set("Access-Control-Allow-Origin", "*")
		header.Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if r.Method != http.MethodPost {
			log.Println("not post, dismissing")
			return
		}

		clientDescrJson, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		cdm := map[string]any{}
		err = json.Unmarshal(clientDescrJson, &cdm)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Println("GET", "/init", cdm)
		clientDescr, ok := cdm["descr"].(string)
		if !ok {
			http.Error(w, "expected descr", http.StatusBadRequest)
			return
		}

		buf, err := base64.StdEncoding.DecodeString(clientDescr)
		if err != nil {
			http.Error(w, "b64 decode failed", http.StatusBadRequest)
			return
		}

		starter, err := runRenderInstance(c, buf)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		b, err := json.Marshal(map[string]any{
			"sessionStarter": base64.StdEncoding.EncodeToString(starter),
		})

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, string(b))
	}))
	http.ListenAndServe(addr, nil)
}
