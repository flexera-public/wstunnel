// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package tunnel

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
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

	"github.com/rightscale/wstunnel/whois"
	"gopkg.in/inconshreveable/log15.v2"
)

var _ fmt.Formatter

// The ReadTimeout and WriteTimeout don't actually work in Go
// https://groups.google.com/forum/#!topic/golang-nuts/oBIh_R7-pJQ
//const cliTout = 300 // http read/write/idle timeout

var RetryError = errors.New("Error sending request, please retry")

const tunnelInactiveKillTimeout = 60 * time.Minute   // close dead tunnels
const tunnelInactiveRefuseTimeout = 10 * time.Minute // refuse requests for dead tunnels

//===== Data Structures =====

const (
	MAX_REQ       = 20 // max queued requests per remote server
	MIN_TOKEN_LEN = 16 // min number of chars in a token
)

type token string

type responseBuffer struct {
	err      error
	response *bytes.Buffer
}

// A request for a remote server
type remoteRequest struct {
	id         int16               // unique (scope=server) request id
	info       string              // http method + uri for debug/logging
	remoteAddr string              // remote address for debug/logging
	buffer     *bytes.Buffer       // request buffer to send
	replyChan  chan responseBuffer // response that got returned, capacity=1!
	deadline   time.Time           // timeout
	log        log15.Logger
}

// A remote server
type remoteServer struct {
	token           token                    // rendez-vous token for debug/logging
	lastId          int16                    // id of last request
	lastActivity    time.Time                // last activity on tunnel
	remoteAddr      string                   // last remote addr of tunnel (debug)
	remoteName      string                   // reverse DNS resolution of remoteAddr
	remoteWhois     string                   // whois lookup of remoteAddr
	requestQueue    chan *remoteRequest      // queue of requests to be sent
	requestSet      map[int16]*remoteRequest // all requests in queue/flight indexed by ID
	requestSetMutex sync.Mutex
	log             log15.Logger
}

type WSTunnelServer struct {
	Port                int                     // port to listen on
	WSTimeout           time.Duration           // timeout on websockets
	HttpTimeout         time.Duration           // timeout for HTTP requests
	Log                 log15.Logger            // logger with "pkg=WStunsrv"
	exitChan            chan struct{}           // channel to tell the tunnel goroutines to end
	serverRegistry      map[token]*remoteServer // active remote servers indexed by token
	serverRegistryMutex sync.Mutex              // mutex to protect map
}

// name Lookups
var whoToken string                      // token for the whois service
var dnsCache = make(map[string]string)   // ip_address -> reverse DNS lookup
var whoisCache = make(map[string]string) // ip_address -> whois lookup
var cacheMutex sync.Mutex

func ipAddrLookup(log log15.Logger, ipAddr string) (dns, who string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	dns, ok := dnsCache[ipAddr]
	if !ok {
		names, _ := net.LookupAddr(ipAddr)
		dns = strings.Join(names, ",")
		dnsCache[ipAddr] = dns
		log.Info("DNS lookup", "addr", ipAddr, "dns", dns)
	}
	// whois lookup
	who, ok = whoisCache[ipAddr]
	if !ok && whoToken != "" {
		who = whois.Whois(ipAddr, whoToken)
		whoisCache[ipAddr] = who
	}
	return
}

//===== Main =====

func NewWSTunnelServer(args []string) *WSTunnelServer {
	wstunSrv := WSTunnelServer{}

	var srvFlag = flag.NewFlagSet("server", flag.ExitOnError)
	srvFlag.IntVar(&wstunSrv.Port, "port", 80, "port for http/ws server to listen on")
	var pidf *string = srvFlag.String("pidfile", "", "path for pidfile")
	var logf *string = srvFlag.String("logfile", "", "path for log file")
	var tout *int = srvFlag.Int("wstimeout", 30, "timeout on websocket in seconds")
	var httpTout *int = srvFlag.Int("httptimeout", 20*60, "timeout for http requests in seconds")
	var slog *string = srvFlag.String("syslog", "", "syslog facility to log to")
	var whoTok *string = srvFlag.String("robowhois", "", "robowhois.com API token")

	srvFlag.Parse(args)

	writePid(*pidf)
	wstunSrv.Log = makeLogger("WStunsrv", *logf, *slog)
	wstunSrv.WSTimeout = calcWsTimeout(*tout)
	whoToken = *whoTok

	wstunSrv.HttpTimeout = time.Duration(*httpTout) * time.Second
	wstunSrv.Log.Info("Setting remote request timeout", "timeout", wstunSrv.HttpTimeout)

	wstunSrv.exitChan = make(chan struct{}, 1)

	return &wstunSrv
}

func (t *WSTunnelServer) Start(listener net.Listener) {
	t.Log.Info(VV)
	if t.serverRegistry != nil {
		return // already started...
	}
	t.serverRegistry = make(map[token]*remoteServer)
	go t.idleTunnelReaper()

	//===== HTTP Server =====

	var httpServer http.Server

	// Convert a handler that takes a tunnel as first arg to a std http handler
	wrap := func(h func(t *WSTunnelServer, w http.ResponseWriter, r *http.Request)) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			h(t, w, r)
		}
	}

	// Reqister handlers with default mux
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/", wrap(payloadHeaderHandler))
	httpMux.HandleFunc("/_token/", wrap(payloadPrefixHandler))
	httpMux.HandleFunc("/_tunnel", wrap(tunnelHandler))
	httpMux.HandleFunc("/_health_check", wrap(checkHandler))
	httpMux.HandleFunc("/_stats", wrap(statsHandler))
	httpServer.Handler = httpMux
	//httpServer.ErrorLog = log15Logger // would like to set this somehow...

	// Read/Write timeouts disabled for now due to bug:
	// https://code.google.com/p/go/issues/detail?id=6410
	// https://groups.google.com/forum/#!topic/golang-nuts/oBIh_R7-pJQ
	//ReadTimeout: time.Duration(cliTout) * time.Second, // read and idle timeout
	//WriteTimeout: time.Duration(cliTout) * time.Second, // timeout while writing response

	// Now create the listener and hook it all up
	if listener == nil {
		t.Log.Info("Listening", "port", t.Port)
		laddr := fmt.Sprintf(":%d", t.Port)
		var err error
		listener, err = net.Listen("tcp", laddr)
		if err != nil {
			t.Log.Crit("Cannot listen", "addr", laddr)
			os.Exit(1)
		}
	} else {
		t.Log.Info("Listener", "addr", listener.Addr().String())
	}
	go func() {
		t.Log.Debug("Server started")
		httpServer.Serve(listener)
		t.Log.Debug("Server ended")
	}()

	go func() {
		<-t.exitChan
		listener.Close()
	}()
}

func (t *WSTunnelServer) Stop() {
	t.exitChan <- struct{}{}
}

//===== Handlers =====

// Handler for health check
func checkHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "WSTUNSRV RUNNING")
}

// Handler for stats
func statsHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request) {
	// let's start by doing a GC to ensure we reclaim file descriptors (?)
	runtime.GC()

	// make a copy of the set of remoteServers
	t.serverRegistryMutex.Lock()
	rss := make([]*remoteServer, 0, len(t.serverRegistry))
	for _, rs := range t.serverRegistry {
		rss = append(rss, rs)
	}
	// print out the number of tunnels
	fmt.Fprintf(w, "tunnels=%d\n", len(t.serverRegistry))
	t.serverRegistryMutex.Unlock()

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
		fmt.Fprintf(w, "\ntunnel%02d_token=%s\n", i, cutToken(t.token))
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
func payloadHeaderHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request) {
	// Token header is canonical source of session info
	tok := r.Header.Get("X-Token")

	// Use wildcard-DNS host header as a backup
	if tok == "" {
		components := strings.SplitN(r.Host, ".", 2)
		tok = components[0]
	}

	if tok == "" {
		t.Log.Info("HTTP Missing X-Token header", "req", r)
		http.Error(w, "Missing X-Token header", 400)
		return
	}

	payloadHandler(t, w, r, token(tok))
}

// Regexp for extracting the tunnel token from the URI
var matchToken = regexp.MustCompile("^/_token/([^/]+)(/.*)")

// payloadPrefixHandler handles payload requests with the tunnel token in a URI prefix.
// Payload requests are requests that are to be forwarded through the tunnel.
func payloadPrefixHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request) {
	reqUrl := r.URL.String()
	m := matchToken.FindStringSubmatch(reqUrl)
	if len(m) != 3 {
		t.Log.Info("HTTP Missing token or URI", "url", reqUrl)
		http.Error(w, "Missing token in URI", 400)
		return
	}
	r.URL, _ = url.Parse(m[2])
	payloadHandler(t, w, r, token(m[1]))
}

// payloadHandler is called by payloadHeaderHandler and payloadPrefixHandler to do the real work.
func payloadHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request, tok token) {
	// create the request object
	req := makeRequest(r, t.HttpTimeout)
	req.log = t.Log.New("token", cutToken(tok))
	//req.token = tok
	//log_token := cutToken(tok)

	req.remoteAddr = r.Header.Get("X-Forwarded-For")
	if req.remoteAddr == "" {
		req.remoteAddr = r.RemoteAddr
	}

	// repeatedly try to get a response
	for tries := 1; tries <= 3; tries += 1 {
		retry := getResponse(t, req, w, r, tok, tries)
		if !retry {
			return
		}
	}
}

// getResponse adds the request to a remote server and then waits to get a response back, and it
// writes it. It returns true if the whole thing needs to be retried and false if we're done
// sucessfully or not)
func getResponse(t *WSTunnelServer, req *remoteRequest, w http.ResponseWriter, r *http.Request,
	tok token, tries int) (retry bool) {
	retry = false

	// get a hold of the remote server
	rs := t.getRemoteServer(token(tok), false)
	if rs == nil {
		req.log.Info("HTTP RCV", "addr", req.remoteAddr, "status", "404",
			"err", "Tunnel not found")
		http.Error(w, "Tunnel not found (or not seen in a long time)", 404)
		return
	}

	// Ensure we retire the request when we pop out of this function
	defer func() {
		rs.RetireRequest(req)
	}()

	// enqueue request
	err := rs.AddRequest(req)
	if err != nil {
		req.log.Info("HTTP RCV", "addr", req.remoteAddr, "status", "504",
			"err", err.Error())
		http.Error(w, err.Error(), 504)
		return
	}
	try := ""
	if tries > 1 {
		try = fmt.Sprintf("(attempt #%d)", tries)
	}
	req.log.Info("HTTP RCV", "verb", r.Method, "url", r.URL,
		"addr", req.remoteAddr, "x-host", r.Header.Get("X-Host"), "try", try)
	// wait for response
	select {
	case resp := <-req.replyChan:
		// if there's no error just respond
		if resp.err == nil {
			code := writeResponse(w, resp.response)
			req.log.Info("HTTP RET", "status", code)
			return
		}
		// if it's a non-retryable error then write the error
		if resp.err != RetryError {
			req.log.Info("HTTP RET",
				"status", "504", "err", resp.err.Error())
			http.Error(w, resp.err.Error(), 504)
		} else {
			// else we're gonna retry
			req.log.Info("WS   retrying", "verb", r.Method, "url", r.URL)
			retry = true
		}
	case <-time.After(t.HttpTimeout):
		// it timed out...
		req.log.Info("HTTP RET", "status", "504", "err", "Tunnel timeout")
		http.Error(w, "Tunnel timeout", 504)
	}
	return
}

// tunnelHandler handles tunnel establishment requests
func tunnelHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		wsHandler(t, w, r)
	} else {
		http.Error(w, "Only GET requests are supported", 400)
	}
}

//===== Helpers =====

// Sanitize the token for logging
func cutToken(tok token) string {
	return string(tok)[0:8] + "..."
}

func (t *WSTunnelServer) getRemoteServer(tok token, create bool) *remoteServer {
	t.serverRegistryMutex.Lock()
	defer t.serverRegistryMutex.Unlock()

	// lookup and return existing remote server
	rs, ok := t.serverRegistry[tok]
	if ok || !create { // return null if create flag is not set
		return rs
	}
	// construct new remote server
	rs = &remoteServer{
		token:        tok,
		requestQueue: make(chan *remoteRequest, MAX_REQ),
		requestSet:   make(map[int16]*remoteRequest),
		log:          log15.New("token", cutToken(tok)),
	}
	t.serverRegistry[tok] = rs
	return rs
}

func (rs *remoteServer) AbortRequests() {
	//logToken := cutToken(rs.tok)
	// end any requests that are queued
l:
	for {
		select {
		case req := <-rs.requestQueue:
			err := fmt.Errorf("Tunnel deleted due to inactivity, request cancelled")
			select {
			case req.replyChan <- responseBuffer{err: err}: // non-blocking send
			default:
			}
		default:
			break l
		}
	}
	idle := time.Since(rs.lastActivity).Minutes()
	rs.log.Info("WS tunnel closed", "inactive[min]", idle)
}

func (rs *remoteServer) AddRequest(req *remoteRequest) error {
	rs.requestSetMutex.Lock()
	defer rs.requestSetMutex.Unlock()
	if req.id < 0 {
		rs.lastId = (rs.lastId + 1) % 32000
		req.id = rs.lastId
		req.log = req.log.New("id", req.id)
	}
	rs.requestSet[req.id] = req
	select {
	case rs.requestQueue <- req:
		// enqueued!
		return nil
	default:
		return errors.New("Too many requests in-flight, tunnel broken?")
	}
}

func (rs *remoteServer) RetireRequest(req *remoteRequest) {
	rs.requestSetMutex.Lock()
	defer rs.requestSetMutex.Unlock()
	delete(rs.requestSet, req.id)
	// TODO: should we close the channel? problem is that a concurrent send on it causes a panic
}

func makeRequest(r *http.Request, httpTimeout time.Duration) *remoteRequest {
	buf := &bytes.Buffer{}
	_ = r.Write(buf)
	return &remoteRequest{
		id:        -1,
		info:      r.Method + " " + r.URL.String(),
		buffer:    buf,
		replyChan: make(chan responseBuffer, 10),
		deadline:  time.Now().Add(httpTimeout),
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
func writeResponse(w http.ResponseWriter, buf *bytes.Buffer) int {
	resp, err := http.ReadResponse(bufio.NewReader(buf), nil)
	if err != nil {
		log15.Info("WriteResponse: can't parse incoming response", "err", err)
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
func (t *WSTunnelServer) idleTunnelReaper() {
	t.Log.Debug("idleTunnelReaper started")
	for {
		t.serverRegistryMutex.Lock()
		for _, rs := range t.serverRegistry {
			if time.Since(rs.lastActivity) > tunnelInactiveKillTimeout {
				rs.log.Warn("Tunnel not seen for a long time, deleting",
					"ago", time.Since(rs.lastActivity))
				// unlink so new tunnels/tokens use a new RemoteServer object
				delete(t.serverRegistry, rs.token)
				go rs.AbortRequests()
			}
		}
		t.serverRegistryMutex.Unlock()
		time.Sleep(time.Minute)
	}
	//t.Log.Debug("idleTunnelReaper ended")
}
