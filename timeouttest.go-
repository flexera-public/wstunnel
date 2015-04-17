package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"
)

var r *int = flag.Int("r", 0, "read timeout")
var w *int = flag.Int("w", 0, "write timeout")

func main() {
	flag.Parse()
	http.HandleFunc("/", slashHandler)
	server := http.Server{
		Addr:         ":8123",
		ReadTimeout:  time.Duration(*r) * time.Second,
		WriteTimeout: time.Duration(*w) * time.Second,
	}
	fmt.Printf("Read timeout: %ds, write timeout: %ds\n", *r, *w)
	server.ListenAndServe()
}

func slashHandler(w http.ResponseWriter, r *http.Request) {
	time.Sleep(10 * time.Second)
	w.Write([]byte("Hello world!\n"))
}
