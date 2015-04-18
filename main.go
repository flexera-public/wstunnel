// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"time"
)

var wsTimeout time.Duration

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s [cli|srv] [-options...]", os.Args[0])
	}
	switch os.Args[1] {
	case "cli":
		wstuncli(os.Args[2:])
	case "srv":
		wstunsrv(os.Args[2:], nil)
	default:
		log.Fatalf("Usage: %s [cli|srv] [-options...]", os.Args[0])
	}
	<-make(chan struct{}, 0)
}

func writePid(file string) {
	if file != "" {
		_ = os.Remove(file)
		pid := os.Getpid()
		f, err := os.Create(file)
		if err != nil {
			log.Fatalf("Can't create pidfile %s: %s", file, err.Error())
		}
		_, err = f.WriteString(strconv.Itoa(pid) + "\n")
		if err != nil {
			log.Fatalf("Can't write to pidfile %s: %s", file, err.Error())
		}
		f.Close()
	}
}

func setLogfile(file string) {
	if file != "" {
		log.Printf("Switching logging to %s", file)
		f, err := os.OpenFile(file, os.O_APPEND+os.O_WRONLY+os.O_CREATE, 0664)
		if err != nil {
			log.Fatalf("Can't create log file %s: %s", file, err.Error())
		}
		log.SetOutput(f)
		log.Printf("Started logging here")
	}
}

func setWsTimeout(tout int) {
	if tout < 3 {
		wsTimeout = 3 * time.Second
	} else if tout > 600 {
		wsTimeout = 600 * time.Second
	} else {
		wsTimeout = time.Duration(tout) * time.Second
	}
	log.Printf("Timeout for websocket keep-alives is %d seconds", wsTimeout/time.Second)
}

// copy http headers over
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
