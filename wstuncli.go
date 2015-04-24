// Copyright (c) 2014 RightScale, Inc. - see LICENSE

// Websockets tunnel client, which runs at the HTTP server end (yes, I know, it's confusing)
// This client connects to a websockets tunnel server and waits to receive HTTP requests
// tunneled through the websocket, then issues these HTTP requests locally to an HTTP server
// grabs the response and ships that back through the tunnel.
//
// This client is highly concurrent: it spawns a goroutine for each received request and issues
// that concurrently to the HTTP server and then sends the response back whenever the HTTP
// request returns. The response can thus go back out of order and multiple HTTP requests can
// be in flight at a time.
//
// This client also sends periodic ping messages through the websocket and expects prompt
// responses. If no response is received, it closes the websocket and opens a new one.
//
// The main limitation of this client is that responses have to go throught the same socket
// that the requests arrived on. Thus, if the websocket dies while an HTTP request is in progress
// it impossible for the response to travel on the next websocket, instead it will be dropped
// on the floor. This should not be difficult to fix, though.
//
// Another limitation is that it keeps a single websocket open and can thus get stuck for
// many seconds until the timeout on the websocket hits and a new one is opened.

package main

import (
	"bufio"
	"bytes"
	"os"
	"regexp"
	//"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/inconshreveable/log15.v2"
)

var _ fmt.Formatter

//===== Main =====

func wstuncli(args []string) chan struct{} {
	var cliFlag = flag.NewFlagSet("client", flag.ExitOnError)
	var token *string = cliFlag.String("token", "", "rendez-vous token identifying this server")
	var tunnel *string = cliFlag.String("tunnel", "",
		"websocket server ws[s]://hostname:port to connect to")
	var server *string = cliFlag.String("server", "http://localhost",
		"local HTTP(S) server to send received requests to")
	var serverRegexp *string = cliFlag.String("regexp", "",
		"regexp for local HTTP(S) server to allow sending received requests to")
	var pidf *string = cliFlag.String("pidfile", "", "path for pidfile")
	var logf *string = cliFlag.String("logfile", "", "path for log file")
	var tout *int = cliFlag.Int("timeout", 30, "timeout on websocket in seconds")

	cliFlag.Parse(args)

	setLogfile(*logf, "")
	writePid(*pidf)
	setWsTimeout(*tout)

	// validate -tunnel
	if *tunnel == "" {
		log15.Crit("Must specify remote tunnel server ws://hostname:port using -tunnel option")
		os.Exit(1)
	}
	if !strings.HasPrefix(*tunnel, "ws://") && !strings.HasPrefix(*tunnel, "wss://") {
		log15.Crit("Remote tunnel (-tunnel option) must begin with ws:// or wss://")
		os.Exit(1)
	}
	*tunnel = strings.TrimSuffix(*tunnel, "/")

	// validate -server
	if *server == "" {
		log15.Crit("Must specify local HTTP server http://hostname:port using -server option")
		os.Exit(1)
	}
	if !strings.HasPrefix(*server, "http://") && !strings.HasPrefix(*server, "https://") {
		log15.Crit("Local server (-server option) must begin with http:// or https://")
		os.Exit(1)
	}
	*server = strings.TrimSuffix(*server, "/")

	// process -regexp
	var serverRe *regexp.Regexp = nil
	if *serverRegexp != "" {
		var err error
		serverRe, err = regexp.Compile(*serverRegexp)
		if err != nil {
			log15.Crit("Can't parse -servers regexp", "err", err.Error())
			os.Exit(1)
		}
	}

	// validate token and timeout
	if *token == "" {
		log15.Crit("Must specify rendez-vous token using -token option")
		os.Exit(1)
	}

	// for test purposes we have a signal that tells wstuncli to exit instead of reopening
	// a fresh connection
	exitChan := make(chan struct{}, 1)

	//===== Loop =====

	// Keep opening websocket connections to tunnel requests
	go func() {
		for {
			d := &websocket.Dialer{
				ReadBufferSize:  100 * 1024,
				WriteBufferSize: 100 * 1024,
			}
			h := make(http.Header)
			h.Add("Origin", *token)
			url := fmt.Sprintf("%s/_tunnel", *tunnel)
			timer := time.NewTimer(10 * time.Second)
			log15.Info("Opening", "url", url)
			ws, resp, err := d.Dial(url, h)
			if err != nil {
				extra := ""
				if resp != nil {
					extra = resp.Status
					buf := make([]byte, 80)
					resp.Body.Read(buf)
					if len(buf) > 0 {
						extra = extra + " -- " + string(buf)
					}
					resp.Body.Close()
				}
				log15.Warn("Error opening connection",
					"err", err.Error(), "info", extra)
			} else {
				// Safety setting
				ws.SetReadLimit(100 * 1024 * 1024)
				// Request Loop
				log15.Info("Handling requests", "server", *server)
				handleWsRequests(ws, *server, serverRe)
			}
			// check whether we need to exit
			select {
			case <-exitChan:
				break
			}

			<-timer.C // ensure we don't open connections too rapidly
		}
	}()

	return exitChan
}

// Main function to handle WS requests: it reads a request from the socket, then forks
// a goroutine to perform the actual http request and return the result
func handleWsRequests(ws *websocket.Conn, server string, serverRe *regexp.Regexp) {
	go pinger(ws)
	for {
		ws.SetReadDeadline(time.Time{}) // separate ping-pong routine does timeout
		t, r, err := ws.NextReader()
		if err != nil {
			log15.Info("WS: ReadMessage", "err", err.Error())
			break
		}
		if t != websocket.BinaryMessage {
			log15.Info("WS: invalid message type", "type", t)
			break
		}
		// give the sender a minute to produce the request
		ws.SetReadDeadline(time.Now().Add(time.Minute))
		// read request id
		var id int16
		_, err = fmt.Fscanf(io.LimitReader(r, 4), "%04x", &id)
		if err != nil {
			log15.Info("WS: cannot read request ID", "err", err.Error())
			break
		}
		// read request itself
		req, err := http.ReadRequest(bufio.NewReader(r))
		if err != nil {
			log15.Info("WS: cannot read request body", "err", err.Error())
			break
		}
		// Hand off to goroutine to finish off while we read the next request
		go finishRequest(ws, server, serverRe, id, req)
	}
	// delay a few seconds to allow for writes to drain and then force-close the socket
	go func() {
		time.Sleep(5 * time.Second)
		ws.Close()
	}()
}

//===== Keep-alive ping-pong =====

// Pinger that keeps connections alive and terminates them if they seem stuck
func pinger(ws *websocket.Conn) {
	// timeout handler sends a close message, waits a few seconds, then kills the socket
	timeout := func() {
		ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(1*time.Second))
		time.Sleep(5 * time.Second)
		ws.Close()
	}
	// timeout timer
	timer := time.AfterFunc(wsTimeout, timeout)
	// pong handler resets last pong time
	ph := func(message string) error {
		timer.Reset(wsTimeout)
		return nil
	}
	ws.SetPongHandler(ph)
	// ping loop, ends when socket is closed...
	for {
		err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsTimeout/3))
		if err != nil {
			break
		}
		time.Sleep(wsTimeout / 3)
	}
	ws.Close()
}

//===== HTTP Header Stuff =====

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
	"Host",
}

//===== HTTP driver and response sender =====

var wsWriterMutex sync.Mutex // mutex to allow a single goroutine to send a response at a time

func finishRequest(ws *websocket.Conn, server string, serverRe *regexp.Regexp,
	id int16, req *http.Request) {

	log15.Info("WS", "id", id, "verb", req.Method, "uri", req.RequestURI)

	// Honor X-Host header
	host := server
	xHost := req.Header.Get("X-Host")
	if xHost != "" {
		if serverRe == nil {
			log15.Info("handleWsRequests: got x-host header but no regexp provided")
			writeResponseMessage(ws, id, concoctResponse(req,
				"X-Host header disallowed by wstunnel cli (no -regexp option)", 403))
			return
		} else if serverRe.FindString(xHost) == xHost {
			host = xHost
		} else {
			log15.Info("handleWsRequests: x-host disallowed by regexp", "x-host", xHost)
			writeResponseMessage(ws, id, concoctResponse(req,
				"X-Host header does not match regexp in wstunnel cli", 403))
			return
		}
	}
	req.Header.Del("X-Host")

	// Construct the URL for the outgoing request
	var err error
	req.URL, err = url.Parse(fmt.Sprintf("%s%s", host, req.RequestURI))
	if err != nil {
		log15.Warn("handleWsRequests: cannot parse requestURI", "err", err.Error())
		writeResponseMessage(ws, id, concoctResponse(req,
			"Cannot parse request URI", 400))
		return
	}
	req.Host = req.URL.Host // we delete req.Header["Host"] further down
	req.RequestURI = ""
	log15.Info("handleWsRequests: issuing request", "url", req.URL.String())

	// Accept self-signed certs
	//tr := &http.Transport{
	//        TLSClientConfig: &tls.Config{
	//                InsecureSkipVerify : true,
	//        },
	//}
	// Issue the request to the HTTP server
	//client := http.Client{Transport: tr}

	// Remove hop-by-hop headers
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	// Issue the request to the HTTP server
	client := http.Client{}
	dump, _ := httputil.DumpRequest(req, false)
	log15.Debug("handleWsRequests: dump",
		"req", strings.Replace(string(dump), "\r\n", " || ", -1))
	resp, err := client.Do(req)
	if err != nil {
		//dump2, _ := httputil.DumpResponse(resp, true)
		//log15.Info("handleWsRequests: request error", "err", err.Error(),
		//	"req", string(dump), "resp", string(dump2))
		log15.Info("handleWsRequests: request error", "err", err.Error())
		writeResponseMessage(ws, id, concoctResponse(req, err.Error(), 502))
		return
	}
	log15.Info("handleWsRequests: response", "status", resp.Status)
	defer resp.Body.Close()

	writeResponseMessage(ws, id, resp)
	log15.Info("handleWsRequests: done")
}

// Write the response message to the websocket
func writeResponseMessage(ws *websocket.Conn, id int16, resp *http.Response) {
	// Get writer's lock
	wsWriterMutex.Lock()
	defer wsWriterMutex.Unlock()
	// Write response into the tunnel
	ws.SetWriteDeadline(time.Now().Add(time.Minute))
	w, err := ws.NextWriter(websocket.BinaryMessage)
	// got an error, reply with a "hey, retry" to the request handler
	if err != nil {
		log15.Warn("ws.NextWriter", "err", err.Error())
		ws.Close()
		return
	}

	// write the request Id
	_, err = fmt.Fprintf(w, "%04x", id)
	if err != nil {
		log15.Warn("handleWsRequests: cannot write request Id", "err", err.Error())
		ws.Close()
		return
	}

	// write the response itself
	err = resp.Write(w)
	if err != nil {
		log15.Warn("handleWsRequests: cannot write response", "err", err.Error())
		ws.Close()
		return
	}

	// done
	err = w.Close()
	if err != nil {
		log15.Warn("handleWsRequests: write-close failed", "err", err.Error())
		ws.Close()
		return
	}
}

// Create an http Response from scratch, there must be a better way that this but I
// don't know what it is
func concoctResponse(req *http.Request, message string, code int) *http.Response {
	r := http.Response{
		Status:     "Bad Gateway", //strconv.Itoa(code),
		StatusCode: code,
		Proto:      req.Proto,
		ProtoMajor: req.ProtoMajor,
		ProtoMinor: req.ProtoMinor,
		Header:     make(map[string][]string),
		Request:    req,
	}
	body := bytes.NewReader([]byte(message))
	r.Body = ioutil.NopCloser(body)
	r.ContentLength = int64(body.Len())
	r.Header.Add("content-type", "text/plain")
	r.Header.Add("date", time.Now().Format(time.RFC1123))
	r.Header.Add("server", "wstunnel")
	return &r
}
