// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
	"fmt"
	"net"
	_ "net/http/pprof"
	"os"
	"strings"

	"github.com/rightscale/wstunnel/tunnel"
	"github.com/rightscale/wstunnel/whois"
	"gopkg.in/inconshreveable/log15.v2"
)

func init() { tunnel.SetVV(VV) } // propagate version

func main() {
	if len(os.Args) < 2 {
		log15.Crit(fmt.Sprintf("Usage: %s [cli|srv|whois|version] [-options...]", os.Args[0]))
		os.Exit(1)
	}
	switch os.Args[1] {
	case "cli":
		err := tunnel.NewWSTunnelClient(os.Args[2:]).Start()
		if err != nil {
			log15.Crit(err.Error())
			os.Exit(1)
		}
	case "srv":
		tunnel.NewWSTunnelServer(os.Args[2:]).Start(nil)
	case "whois":
		lookupWhois(os.Args[2:])
		os.Exit(0)
	case "version", "-version", "--version":
		log15.Crit(VV)
		os.Exit(1)
	default:
		log15.Crit(fmt.Sprintf("Usage: %s [cli|srv] [-options...]", os.Args[0]))
		os.Exit(1)
	}
	<-make(chan struct{}, 0)
}

func lookupWhois(args []string) {
	if len(args) != 2 {
		log15.Crit("Usage: %s whois <whois-token> <ip-address>", os.Args[0])
		os.Exit(1)
	}
	what := args[1]
	names, _ := net.LookupAddr(what)
	log15.Info("DNS   ", "addr", what, "dns", strings.Join(names, ","))
	log15.Info("WHOIS ", "addr", what, "dns", whois.Whois(what, args[0]))
}
