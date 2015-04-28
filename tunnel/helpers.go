package tunnel

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"gopkg.in/inconshreveable/log15.v2"
)

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
func makeLogger(pkg, file, facility string) log15.Logger {
	log := log15.New("pkg", pkg)
	if file != "" {
		if facility != "" {
			log.Crit("Can't log to syslog and logfile simultaneously")
			os.Exit(1)
		}
		log.Info("Switching logging", "file", file)
		h, err := log15.FileHandler(file, log15.TerminalFormat())
		if err != nil {
			log.Crit("Can't create log file", "file", file, "err", err.Error())
			os.Exit(1)
		}
		log15.Root().SetHandler(h)
		log.Info("Started logging here")
	} else if facility != "" {
		log.Info("Switching logging to syslog", "facility", facility)
		h, err := log15.SyslogHandler(facility, log15.TerminalFormat())
		if err != nil {
			log.Crit("Can't connect to syslog", "err", err.Error())
			os.Exit(1)
		}
		log15.Root().SetHandler(h)
		log.Info("Started logging here")
	} else {
		log.Info("WStunnel starting")
	}
	return log
}

func calcWsTimeout(tout int) time.Duration {
	var wsTimeout time.Duration
	if tout < 3 {
		wsTimeout = 3 * time.Second
	} else if tout > 600 {
		wsTimeout = 600 * time.Second
	} else {
		wsTimeout = time.Duration(tout) * time.Second
	}
	log15.Info("Websocket keep-alive timeout", "timeout", wsTimeout)
	return wsTimeout
}

// copy http headers over
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
