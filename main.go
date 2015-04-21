// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
	"fmt"
	"gopkg.in/inconshreveable/log15.v2"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"time"
)

var wsTimeout time.Duration

func main() {
	if len(os.Args) < 2 {
		log15.Crit(fmt.Sprintf("Usage: %s [cli|srv] [-options...]", os.Args[0]))
		os.Exit(1)
	}
	switch os.Args[1] {
	case "cli":
		wstuncli(os.Args[2:])
	case "srv":
		wstunsrv(os.Args[2:], nil)
	case "version", "-version", "--version":
		log15.Crit(VV)
		os.Exit(1)
	default:
		log15.Crit(fmt.Sprintf("Usage: %s [cli|srv] [-options...]", os.Args[0]))
		os.Exit(1)
	}
	<-make(chan struct{}, 0)
}

func writePid(file string) {
	if file != "" {
		_ = os.Remove(file)
		pid := os.Getpid()
		f, err := os.Create(file)
		if err != nil {
			log15.Crit("Can't create pidfile", "file", file, "err", err.Error())
			os.Exit(1)
		}
		_, err = f.WriteString(strconv.Itoa(pid) + "\n")
		if err != nil {
			log15.Crit("Can't write to pidfile", "file", file, "err", err.Error())
			os.Exit(1)
		}
		f.Close()
	}
}

// Set logging to use the file or syslog, one of the them must be "" else an error ensues
func setLogfile(file, facility string) {
	if file != "" {
		if facility != "" {
			log15.Crit("Can't log to syslog and logfile simultaneously")
			os.Exit(1)
		}
		log15.Info("Switching logging", "file", file)
		h, err := log15.FileHandler(file, log15.TerminalFormat())
		if err != nil {
			log15.Crit("Can't create log file", "file", file, "err", err.Error())
			os.Exit(1)
		}
		log15.Root().SetHandler(h)
		log15.Info("Started logging here")
	} else if facility != "" {
		log15.Info("Switching logging to syslog", "facility", facility)
		h, err := log15.SyslogHandler(facility, log15.TerminalFormat())
		if err != nil {
			log15.Crit("Can't connect to syslog", "err", err.Error())
			os.Exit(1)
		}
		log15.Root().SetHandler(h)
		log15.Info("Started logging here")
	} else {
		log15.Info("WStunnel starting")
	}
}

/*
var log15Logger = func() log.Logger {
	writer :=
	return log.New(writer, "", 0)
}()
*/

func setWsTimeout(tout int) {
	if tout < 3 {
		wsTimeout = 3 * time.Second
	} else if tout > 600 {
		wsTimeout = 600 * time.Second
	} else {
		wsTimeout = time.Duration(tout) * time.Second
	}
	log15.Info("Websocket keep-alive timeout", "timeout", wsTimeout)
}

// copy http headers over
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
