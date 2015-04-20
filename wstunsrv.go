// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/syslog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var _ fmt.Formatter

// The ReadTimeout and WriteTimeout don't actually work in Go
// https://groups.google.com/forum/#!topic/golang-nuts/oBIh_R7-pJQ
//const cliTout = 300 // http read/write/idle timeout

var RetryError = errors.New("Error sending request, please retry")

//===== Data Structures =====

const (
	MAX_REQ       = 20 // max queued requests per remote server
	MIN_TOKEN_LEN = 16 // min number of chars in a token
)

type Token string

type ResponseBuffer struct {
	err      error
	response *bytes.Buffer
}

// A request for a remote server
type RemoteRequest struct {
	id         int16               // unique (scope=server) request id
	token      Token               // rendez-vous token for debug/logging
	info       string              // http method + uri for debug/logging
	remoteAddr string              // remote address for debug/logging
	buffer     *bytes.Buffer       // request buffer to send
	replyChan  chan ResponseBuffer // response that got returned, capacity=1!
	deadline   time.Time           // timeout
}

// A remote server
type RemoteServer struct {
	token           Token                    // rendez-vous token for debug/logging
	lastId          int16                    // id of last request
	lastActivity    time.Time                // last activity on tunnel
	remoteAddr      string                   // last remote addr of tunnel (debug)
	remoteName      string                   // reverse DNS resolution of remoteAddr
	remoteWhois     string                   // whois lookup of remoteAddr
	requestQueue    chan *RemoteRequest      // queue of requests to be sent
	requestSet      map[int16]*RemoteRequest // all requests in queue/flight indexed by ID
	requestSetMutex sync.Mutex
}

// The set of remote servers we know about
var serverRegistry = make(map[Token]*RemoteServer) // active remote servers indexed by token
var serverRegistryMutex sync.Mutex

// name Lookups
var whoToken string                      // token for the whois service
var dnsCache = make(map[string]string)   // ip_address -> reverse DNS lookup
var whoisCache = make(map[string]string) // ip_address -> whois lookup
var cacheMutex sync.Mutex

func ipAddrLookup(ipAddr string) (dns, whois string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	dns, ok := dnsCache[ipAddr]
	if !ok {
		names, _ := net.LookupAddr(ipAddr)
		dns = strings.Join(names, ",")
		dnsCache[ipAddr] = dns
		log.Printf("DNS lookup: %s -> %s", ipAddr, dns)
	}
	// whois lookup
	whois, ok = whoisCache[ipAddr]
	if !ok && whoToken != "" {
		whois = Whois(ipAddr, whoToken)
		whoisCache[ipAddr] = whois
	}
	return
}

//===== Main =====

var httpTimeout int

func wstunsrv(args []string, listener net.Listener) chan struct{} {
	var srvFlag = flag.NewFlagSet("server", flag.ExitOnError)
	var port *int = srvFlag.Int("port", 80, "port for http/ws server to listen on")
	var pidf *string = srvFlag.String("pidfile", "", "path for pidfile")
	var logf *string = srvFlag.String("logfile", "", "path for log file")
	var tout *int = srvFlag.Int("wstimeout", 30, "timeout on websocket in seconds")
	var httpTout *int = srvFlag.Int("httptimeout", 20*60, "timeout for http requests in seconds")
	var slog *string = srvFlag.String("syslog", "", "syslog facility to log to")
	var whoTok *string = srvFlag.String("robowhois", "", "robowhois.com API token")
	var lookup *string = srvFlag.String("lookup", "", "IP address to lookup in robowhois (doesn't run tunnel)")

	srvFlag.Parse(args)

	writePid(*pidf)
	setLogfile(*logf)
	setWsTimeout(*tout)
	whoToken = *whoTok

	if *slog != "" {
		log.Printf("Switching logging to syslog %s", *slog)
		if *logf != "" {
			log.Fatal("Can't log to syslog and logfile simultaneously")
		}
		f, err := syslog.New(syslog.LOG_INFO, *slog)
		if err != nil {
			log.Fatalf("Can't connect to syslog")
		}
		log.SetOutput(f)
		log.SetFlags(0) // syslog already has timestamp
		log.Printf("Started logging here")
	}

	if *lookup != "" {
		names, _ := net.LookupAddr(*lookup)
		fmt.Printf("DNS   %s -> %s\n", *lookup, strings.Join(names, ","))
		fmt.Printf("WHOIS %s -> %s\n", *lookup, Whois(*lookup, whoToken))
		os.Exit(0)
	}

	httpTimeout = *httpTout
	log.Printf("Timeout for remote requests is %d seconds", httpTimeout)

	exitChan := make(chan struct{}, 1)

	go idleTunnelReaper()

	//===== HTTP Server =====

	// Reqister handlers with default mux
	http.HandleFunc("/", payloadHeaderHandler)
	http.HandleFunc("/_token/", payloadPrefixHandler)
	http.HandleFunc("/_tunnel", tunnelHandler)
	http.HandleFunc("/_health_check", checkHandler)
	http.HandleFunc("/_stats", statsHandler)

	// Now create the HTTP server and let it do its thing
	if listener == nil {
		log.Printf("Listening on port %d\n", *port)
		laddr := fmt.Sprintf(":%d", *port)
		var err error
		listener, err = net.Listen("tcp", laddr)
		if err != nil {
			log.Fatalf("Cannot listen on %s", laddr)
		}
	}
	server := http.Server{}
	go server.Serve(listener)
	// Read/Write timeouts disabled for now due to bug:
	// https://code.google.com/p/go/issues/detail?id=6410
	// https://groups.google.com/forum/#!topic/golang-nuts/oBIh_R7-pJQ
	//ReadTimeout: time.Duration(cliTout) * time.Second, // read and idle timeout
	//WriteTimeout: time.Duration(cliTout) * time.Second, // timeout while writing response

	go func() {
		<-exitChan
		listener.Close()
	}()

	return exitChan
}

//===== Handlers =====

// Handler for health check
func checkHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "WSTUNSRV RUNNING")
}

// Handler for stats
func statsHandler(w http.ResponseWriter, r *http.Request) {
	// let's start by doing a GC to ensure we reclaim file descriptors (?)
	runtime.GC()

	// make a copy of the set of remoteServers
	serverRegistryMutex.Lock()
	rss := make([]*RemoteServer, 0, len(serverRegistry))
	for _, rs := range serverRegistry {
		rss = append(rss, rs)
	}
	serverRegistryMutex.Unlock()

	// print out the number of tunnels
	fmt.Fprintf(w, "tunnels=%d\n", len(serverRegistry))

	// cut off here if not called from localhost
	addr := r.Header.Get("X-Forwarded-For")
	if addr == "" {
		addr = r.RemoteAddr
	}
	if !strings.HasPrefix(addr, "127.0.0.1") {
		fmt.Fprintln(w, "More stats available when called from localhost...")
		return
	}

	reqPending := 0
	badTunnels := 0
	for i, t := range rss {
		fmt.Fprintf(w, "\ntunnel%02d_token=%s\n", i, CutToken(t.token))
		fmt.Fprintf(w, "tunnel%02d_req_pending=%d\n", i, len(t.requestSet))
		reqPending += len(t.requestSet)
		fmt.Fprintf(w, "tunnel%02d_tun_addr=%s\n", i, t.remoteAddr)
		if t.remoteName != "" {
			fmt.Fprintf(w, "tunnel%02d_tun_dns=%s\n", i, t.remoteName)
		}
		if t.remoteWhois != "" {
			fmt.Fprintf(w, "tunnel%02d_tun_whois=%s\n", i, t.remoteWhois)
		}
		if t.lastActivity.IsZero() {
			fmt.Fprintf(w, "tunnel%02d_idle_secs=NaN\n", i)
			badTunnels += 1
		} else {
			fmt.Fprintf(w, "tunnel%02d_idle_secs=%.1f\n", i,
				time.Since(t.lastActivity).Seconds())
			if time.Since(t.lastActivity).Seconds() > 60 {
				badTunnels += 1
			}
		}
		if len(t.requestSet) > 0 {
			t.requestSetMutex.Lock()
			if r, ok := t.requestSet[t.lastId]; ok {
				fmt.Fprintf(w, "tunnel%02d_cli_addr=%s\n", i, r.remoteAddr)
			}
			t.requestSetMutex.Unlock()
		}
	}
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "req_pending=%d\n", reqPending)
	fmt.Fprintf(w, "dead_tunnels=%d\n", badTunnels)
}

// payloadHeaderHandler handles payload requests with the tunnel token in the Host header.
// Payload requests are requests that are to be forwarded through the tunnel.
func payloadHeaderHandler(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Token")
	if token == "" {
		log.Printf("Missing X-Token header: %#v", r)
		http.Error(w, "Missing X-Token header", 400)
		return
	}
	payloadHandler(w, r, Token(token))
}

// Regexp for extracting the tunnel token from the URI
var matchToken = regexp.MustCompile("^/_token/([^/]+)(/.*)")

// payloadPrefixHandler handles payload requests with the tunnel token in a URI prefix.
// Payload requests are requests that are to be forwarded through the tunnel.
func payloadPrefixHandler(w http.ResponseWriter, r *http.Request) {
	reqUrl := r.URL.String()
	m := matchToken.FindStringSubmatch(reqUrl)
	if len(m) != 3 {
		log.Printf("Missing token or URI: %s", reqUrl)
		http.Error(w, "Missing token in URI", 400)
		return
	}
	r.URL, _ = url.Parse(m[2])
	payloadHandler(w, r, Token(m[1]))
}

// payloadHandler is called by payloadHeaderHandler and payloadPrefixHandler to do the real work.
func payloadHandler(w http.ResponseWriter, r *http.Request, token Token) {
	// create the request object
	req := MakeRequest(r)
	req.token = token
	log_token := CutToken(token)

	req.remoteAddr = r.Header.Get("X-Forwarded-For")
	if req.remoteAddr == "" {
		req.remoteAddr = r.RemoteAddr
	}

	// repeatedly try to get a response
	var rs *RemoteServer
Tries:
	for tries := 1; tries <= 3; tries += 1 {
		// get a hold of the remote server
		rs = GetRemoteServer(Token(token))

		// enqueue request
		err := rs.AddRequest(req)
		if err != nil {
			log.Printf("%s: HTTP RCV %s %s (%s) RET status=504 error: %s",
				log_token, r.Method, r.URL, req.remoteAddr, err.Error())
			http.Error(w, err.Error(), 504)
			break Tries
		}
		try := ""
		if tries > 1 {
			try = fmt.Sprintf("(attempt #%d)", tries)
		}
		log.Printf("%s #%d: HTTP RCV %s %s (%s) %s", log_token, req.id, r.Method, r.URL,
			req.remoteAddr, try)
		// wait for response
		select {
		case resp := <-req.replyChan:
			// if there's no error just respond
			if resp.err == nil {
				code := WriteResponse(w, resp.response)
				log.Printf("%s #%d: HTTP RET status=%d\n", log_token, req.id, code)
				break Tries
			}
			// if it's a non-retryable error then write the error
			if resp.err != RetryError {
				log.Printf("%s #%d: HTTP RET status=504 error=%s\n",
					log_token, req.id, resp.err.Error())
				http.Error(w, resp.err.Error(), 504)
				break Tries
			}
			// else we're gonna retry
			log.Printf("%s #%d: retrying %s %s\n", log_token, req.id, r.Method, r.URL)
		case <-time.After(time.Duration(httpTimeout) * time.Second):
			// it timed out...
			log.Printf("%s #%d: HTTP RET status=504 error=Tunnel timeout",
				log_token, req.id)
			http.Error(w, "Tunnel timeout", 504)
			break Tries
		}
	}

	// Retire the request, since we've responded...
	rs.RetireRequest(req)
}

// tunnelHandler handles tunnel establishment requests
func tunnelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		wsHandler(w, r)
	} else {
		http.Error(w, "Only GET requests are supported", 400)
	}
}

//===== Helpers =====

// Sanitize the token for logging
var cutRe = regexp.MustCompile("[^_]{16}==$")

func CutToken(token Token) string {
	return cutRe.ReplaceAllString(string(token), "...")
}

func GetRemoteServer(token Token) *RemoteServer {
	serverRegistryMutex.Lock()
	defer serverRegistryMutex.Unlock()

	// lookup and return existing remote server
	rs, ok := serverRegistry[token]
	if ok {
		return rs
	}
	// construct new remote server
	rs = &RemoteServer{
		token:        token,
		requestQueue: make(chan *RemoteRequest, MAX_REQ),
		requestSet:   make(map[int16]*RemoteRequest),
	}
	serverRegistry[token] = rs
	return rs
}

func (rs *RemoteServer) AbortRequests() {
	logToken := CutToken(rs.token)
	// end any requests that are queued
l:
	for {
		select {
		case req := <-rs.requestQueue:
			select {
			case req.replyChan <- ResponseBuffer{err: RetryError}: // non-blocking send
				log.Printf("%s %d: WS tunnel inactive timeout causes retry",
					logToken, req.id)
			default:
			}
		default:
			break l
		}
	}
	idle := time.Since(rs.lastActivity).Minutes()
	log.Printf("%s WS tunnel closed due to inactivity for %.0f minutes", logToken, idle)
}

func (rs *RemoteServer) AddRequest(req *RemoteRequest) error {
	rs.requestSetMutex.Lock()
	defer rs.requestSetMutex.Unlock()
	if req.id < 0 {
		rs.lastId = (rs.lastId + 1) % 32000
		req.id = rs.lastId
	}
	rs.requestSet[req.id] = req
	select {
	case rs.requestQueue <- req:
		// enqueued!
		return nil
	default:
		return errors.New("Too many requests in-flight")
	}
}

func (rs *RemoteServer) RetireRequest(req *RemoteRequest) {
	rs.requestSetMutex.Lock()
	defer rs.requestSetMutex.Unlock()
	delete(rs.requestSet, req.id)
	// TODO: should we close the channel? problem is that a concurrent send on it causes a panic
}

func MakeRequest(r *http.Request) *RemoteRequest {
	buf := &bytes.Buffer{}
	_ = r.Write(buf)
	return &RemoteRequest{
		id:        -1,
		info:      r.Method + " " + r.URL.String(),
		buffer:    buf,
		replyChan: make(chan ResponseBuffer, 10),
		deadline:  time.Now().Add(time.Duration(httpTimeout) * time.Second),
	}

}

// censoredHeaders, these are removed from the response before forwarding
var censoredHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
}

// Write an HTTP response from a byte buffer into a ResponseWriter
func WriteResponse(w http.ResponseWriter, buf *bytes.Buffer) int {
	resp, err := http.ReadResponse(bufio.NewReader(buf), nil)
	if err != nil {
		log.Printf("WriteResponse: can't parse incoming response: %s", err)
		w.WriteHeader(506)
		return 506
	}
	for _, h := range censoredHeaders {
		resp.Header.Del(h)
	}
	// write the response
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return resp.StatusCode
}

// idleTunnelReaper should be run in a goroutine to kill tunnels that are idle for a long time
func idleTunnelReaper() {
	for {
		serverRegistryMutex.Lock()
		for _, rs := range serverRegistry {
			if time.Since(rs.lastActivity) > 60*time.Minute {
				go func() {
					// unlink so new tunnels/tokens use a new RemoteServer object
					delete(serverRegistry, rs.token)
					rs.AbortRequests()
				}()
			}
		}
		serverRegistryMutex.Unlock()
		time.Sleep(time.Minute)
	}
}
