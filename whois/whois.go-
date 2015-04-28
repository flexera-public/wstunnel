// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
	"log"
	"fmt"
	"net"
	"regexp"
	"time"
	"io/ioutil"
)

var orgNameRe = regexp.MustCompile("Org[^a-zA-Z]?Name[^a-zA-Z]*(.*)")
var refServerRe = regexp.MustCompile("ReferralServer.*?([-a-zA-Z0-9.]+\\.[-a-zA-Z0-9]+)")

func queryWhois(server, ipAddr string) (referralServer, orgName string) {
        // open connection to whois server
        conn, err := net.DialTimeout("tcp", net.JoinHostPort(server, "43"), 5*time.Second)
        if err != nil {
                log.Printf("whois: timeout connecting to %s", server)
                return
        }
        // send query with appropriate timeouts
        conn.SetWriteDeadline(time.Now().Add(1*time.Second))
        conn.SetReadDeadline(time.Now().Add(3*time.Second))
        fmt.Fprintf(conn, "%s\r\n", ipAddr)
        // read response all at once
        buf, err := ioutil.ReadAll(conn)
        if err != nil {
                log.Printf("whois: timeout reading response from %s", server)
                return
        }
        conn.Close()
        // find organization name
        matches := orgNameRe.FindSubmatch(buf)
        if matches != nil {
                orgName = string(matches[1])
                log.Printf("whois: response from %s has OrgName=%s", server, orgName)
        } else {
                log.Printf("whois: response from %s has no OrgName", server)
        }
        // find referral server
        matches = refServerRe.FindSubmatch(buf)
        if matches != nil {
                referralServer = string(matches[1])
                log.Printf("whois: response from %s has ReferralServer=%s", server, referralServer)
        } else {
                log.Printf("whois: response from %s has no ReferralServer", server)
        }
        return
}

func Whois(ipAddr string) (orgName string) {
        log.Printf("whois: starting query for %s", ipAddr)
        referralServer, orgName := queryWhois("whois.arin.net", ipAddr)
        if referralServer == "" && orgName == "" {
                return
        }
        for referralServer != "" {
                referralServer, orgName = queryWhois(referralServer, ipAddr)
        }
        return
}

//func main() {
//        fmt.Printf("%s -> %s\n", os.Args[1], Whois(os.Args[1]))
//}

