// +build !windows

package tunnel

import (
	"log/syslog"
	"os"

	log15 "gopkg.in/inconshreveable/log15.v2"
)

// Set logging to use the file or syslog, one of the them must be "" else an error ensues
func makeLogger(pkg, file, facility string) log15.Logger {
	log := log15.New("pkg", pkg)
	if file != "" {
		if facility != "" {
			log.Crit("Can't log to syslog and logfile simultaneously")
			os.Exit(1)
		}
		log.Info("Switching logging", "file", file)
		h, err := log15.FileHandler(file, SimpleFormat(true))
		if err != nil {
			log.Crit("Can't create log file", "file", file, "err", err.Error())
			os.Exit(1)
		}
		log15.Root().SetHandler(h)
		log.Info("Started logging here")
	} else if facility != "" {
		log.Info("Switching logging to syslog", "facility", facility)
		h, err := log15.SyslogHandler(syslog.LOG_INFO, facility, SimpleFormat(false))
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
