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
	//"crypto/tls"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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
	var pidf *string = cliFlag.String("pidfile", "", "path for pidfile")
	var logf *string = cliFlag.String("logfile", "", "path for log file")
	var tout *int = cliFlag.Int("timeout", 30, "timeout on websocket in seconds")

	cliFlag.Parse(args)

	writePid(*pidf)
	setLogfile(*logf)
	setWsTimeout(*tout)

	// validate -tunnel
	if *tunnel == "" {
		log.Fatal("Must specify remote tunnel server ws://hostname:port using -tunnel option")
	}
	if !strings.HasPrefix(*tunnel, "ws://") && !strings.HasPrefix(*tunnel, "wss://") {
		log.Fatal("Remote tunnel (-tunnel option) must begin with ws:// or wss://")
	}
	*tunnel = strings.TrimSuffix(*tunnel, "/")

	// validate -server
	if *server == "" {
		log.Fatal("Must specify local HTTP server http://hostname:port using -server option")
	}
	if !strings.HasPrefix(*server, "http://") && !strings.HasPrefix(*server, "https://") {
		log.Fatal("Local server (-server option) must begin with http:// or https://")
	}
	*server = strings.TrimSuffix(*server, "/")

	// validate token and timeout
	if *token == "" {
		log.Fatal("Must specify rendez-vous token using -token option")
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
			log.Printf("Opening %s\n", url)
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
				log.Printf("Error opening connection: %s -- %s", err.Error(), extra)
			} else {
				// Safety setting
				ws.SetReadLimit(100 * 1024 * 1024)
				// Request Loop
				handleWsRequests(ws, *server)
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
func handleWsRequests(ws *websocket.Conn, server string) {
	go pinger(ws)
	for {
		ws.SetReadDeadline(time.Time{}) // separate ping-pong routine does timeout
		t, r, err := ws.NextReader()
		if err != nil {
			log.Printf("WS: ReadMessage %s", err.Error())
			break
		}
		if t != websocket.BinaryMessage {
			log.Printf("WS: invalid message type=%d", t)
			break
		}
		// give the sender a minute to produce the request
		ws.SetReadDeadline(time.Now().Add(time.Minute))
		// read request id
		var id int16
		_, err = fmt.Fscanf(io.LimitReader(r, 4), "%04x", &id)
		if err != nil {
			log.Printf("WS: cannot read request ID: %s", err.Error())
			break
		}
		// read request itself
		req, err := http.ReadRequest(bufio.NewReader(r))
		if err != nil {
			log.Printf("WS: cannot read request body: %s", err.Error())
			break
		}
		// Hand off to goroutine to finish off while we read the next request
		go finishRequest(ws, server, id, req)
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

func finishRequest(ws *websocket.Conn, server string, id int16, req *http.Request) {
	log.Printf("WS #%d: %s %s\n", id, req.Method, req.RequestURI)
	// Construct the URL for the outgoing request
	var err error
	req.URL, err = url.Parse(fmt.Sprintf("%s%s", server, req.RequestURI))
	if err != nil {
		log.Printf("handleWsRequests: cannot parse requestURI: %s", err.Error())
		return
	}
	req.RequestURI = ""
	log.Printf("handleWsRequests: issuing request to %s", req.URL.String())

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
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("handleWsRequests: request error: %s\n", err.Error())
		log.Println("=== REQ =======")
		log.Println(string(dump))
		log.Println("=== RESP ======")
		resp = concoctResponse(req, err.Error(), 502)
		dump, _ = httputil.DumpResponse(resp, true)
		log.Println(string(dump))
		log.Println("==========")
	} else {
		log.Printf("handleWsRequests: got %s\n", resp.Status)
	}
	defer resp.Body.Close()
	// Get writer's lock
	wsWriterMutex.Lock()
	defer wsWriterMutex.Unlock()
	// Write response into the tunnel
	ws.SetWriteDeadline(time.Now().Add(time.Minute))
	w, err := ws.NextWriter(websocket.BinaryMessage)
	// got an error, reply with a "hey, retry" to the request handler
	if err != nil {
		log.Printf("ws.NextWriter: %s", err.Error())
		ws.Close()
		return
	}

	// write the request Id
	_, err = fmt.Fprintf(w, "%04x", id)
	if err != nil {
		log.Printf("handleWsRequests: cannot write request Id:  %s", err.Error())
		ws.Close()
		return
	}

	// write the response itself
	err = resp.Write(w)
	if err != nil {
		log.Printf("handleWsRequests: cannot write response:  %s", err.Error())
		ws.Close()
		return
	}

	// done
	err = w.Close()
	if err != nil {
		log.Printf("handleWsRequests: write-close failed: %s", err.Error())
		ws.Close()
		return
	}
	log.Printf("handleWsRequests: done\n")
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
