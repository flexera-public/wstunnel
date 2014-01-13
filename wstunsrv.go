// Copyright (c) 2013 Thorsten von Eicken

package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"net/textproto"
	"net/url"
        _ "net/http/pprof"
)

var _ fmt.Formatter

var port *int = flag.Int("port", 80, "port for http/ws server to listen on")
var pidf *string = flag.String("pidfile", "", "path for pidfile")
var logf *string = flag.String("logfile", "", "path for log file")
var tout *int    = flag.Int("timeout", 30, "timeout on websocket in seconds")
var wsTimeout time.Duration

var RetryError = errors.New("Error sending request, please retry")

//===== Data Structures =====

const (
        MAX_REQ         = 10                    // max queued requests per remote server
)

type Token string

type ResponseBuffer struct {
        err             error
        response        *bytes.Buffer
}

// A request for a remote server
type RemoteRequest struct {
        id              int16                   // unique (scope=server) request id
        token           Token                   // rendez-vous token for debug/logging
        info            string                  // http method + uri for debug/logging
        buffer          *bytes.Buffer           // request buffer to send
        replyChan       chan ResponseBuffer     // response that got returned, capacity=1!
        deadline        time.Time               // timeout
}

// A remote server
type RemoteServer struct {
        token           Token                   // rendez-vous token for debug/logging
        lastId          int16                   // id of last request
        requestQueue    chan *RemoteRequest     // queue to be sent
        requestSet      map[int16]*RemoteRequest // all requests in queue/flight indexed by ID
        requestSetMutex sync.Mutex
}

// The set of remote servers we know about
var serverRegistry      = make(map[Token]*RemoteServer) // active remote servers indexed by token
var serverRegistryMutex sync.Mutex

//===== Main =====

func main() {
	flag.Parse()

        if *pidf != "" {
                _ = os.Remove(*pidf)
                pid := os.Getpid()
                f, err := os.Create(*pidf)
                if err != nil {
                        log.Fatalf("Can't create pidfile %s: %s", *pidf, err.Error())
                }
                _, err = f.WriteString(strconv.Itoa(pid) + "\n")
                if err != nil {
                        log.Fatalf("Can't write to pidfile %s: %s", *pidf, err.Error())
                }
                f.Close()
        }

        if *logf != "" {
                log.Printf("Switching logging to %s", *logf)
                f, err := os.OpenFile(*logf, os.O_APPEND + os.O_WRONLY + os.O_CREATE, 0664)
                if err != nil {
                        log.Fatalf("Can't create log file %s: %s", *logf, err.Error())
                }
                log.SetOutput(f)
                log.Printf("Started logging here")
        }

        if *tout < 3 {
                *tout = 3
        }
        if *tout > 600 {
                *tout = 600
        }
        wsTimeout = time.Duration(*tout) * time.Second

        //===== HTTP Server =====

        // Reqister handlers with default mux
        http.HandleFunc("/", payloadHeaderHandler)
        http.HandleFunc("/_token/", payloadPrefixHandler)
        http.HandleFunc("/_tunnel", tunnelHandler)

        // Now create the HTTP server and let it do its thing
        log.Printf("Listening on port %d\n", *port)
        laddr := fmt.Sprintf(":%d", *port)
        log.Fatal(http.ListenAndServe(laddr, nil))
}

//===== Handlers =====

// Handler for payload requests with the token in the Host header
func payloadHeaderHandler(w http.ResponseWriter, r *http.Request) {
        token := r.Header.Get("X-Token")
        if token == "" {
                log.Printf("Missing X-Token header: %#v", r)
                http.Error(w, "Missing X-Token header", 400)
                return
        }
        payloadHandler(w, r, Token(token))
}

// Handler for payload requests with the token in the URI
var matchToken = regexp.MustCompile("^/_token/([^/]+)(/.*)")

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

// Handler for payload requests with the token already sorted out
func payloadHandler(w http.ResponseWriter, r *http.Request, token Token) {
        log.Printf("HTTP->%s: %s %s\n", token, r.Method, r.URL)

        // get a hold of the remote server
        rs := GetRemoteServer(Token(token))

        // create the request object
        req := MakeRequest(r)
        req.token = Token(token)

        // repeatedly try to get a response
        for {
                // enqueue request
                rs.AddRequest(req)
                // wait for response
                resp := <-req.replyChan
                // if there's no error just respond
                if resp.err == nil {
                        log.Printf("HTTP<-%s\n", token)
                        WriteResponse(w, resp.response)
                        break
                }
                // if it's a non-retryable error then write the error
                if resp.err != RetryError {
                        log.Printf("HTTP<-%s: %s\n", resp.err.Error())
                        http.Error(w, resp.err.Error(), 504)
                        break
                }
                // else we're gonna retry
        }

        // Retire the request, since we've responded...
        rs.RetireRequest(req)
}

// Handler for tunnel establishment requests
func tunnelHandler(w http.ResponseWriter, r *http.Request) {
        if r.Method == "GET" {
                wsHandler(w, r)
        } else {
                //lpHandler(w, r)
        }
}

//===== Helpers =====

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
                token:          token,
                requestQueue:   make(chan *RemoteRequest, MAX_REQ),
                requestSet:     make(map[int16]*RemoteRequest),
        }
        serverRegistry[token] = rs
        return rs
}

func (rs *RemoteServer) AddRequest(req *RemoteRequest) {
        rs.requestSetMutex.Lock()
        defer rs.requestSetMutex.Unlock()
        if req.id < 0 {
                rs.lastId = (rs.lastId+1) % 32000
                req.id = rs.lastId
        }
        rs.requestSet[req.id] = req
        rs.requestQueue <- req
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
                id: -1,
                info: r.Method + " " + r.RequestURI,
                buffer: buf,
                replyChan: make(chan ResponseBuffer, 10),
                deadline: time.Now().Add(3*time.Minute),
        }

}

// Write an HTTP response from a byte buffer into a ResponseWriter
func WriteResponse(w http.ResponseWriter, resp *bytes.Buffer) {
        head, err := resp.ReadString(0xA)
        heads := strings.SplitN(head, " ", 3)
        if len(heads) != 3 {
                log.Printf("WriteResponse: response heading has only %d fields", len(heads))
                w.WriteHeader(506)
                return
        }
        statusCode, _ := strconv.Atoi(heads[1])

        // Parse the header from the response text
        buf := bufio.NewReader(resp)
        header, err := textproto.NewReader(buf).ReadMIMEHeader()
        if (err != nil) {
                log.Printf("WriteResponse: bad response header: %s", err.Error())
                w.WriteHeader(506)
                return
        }
        log.Printf("HTTP<-%d %s bytes %s", statusCode, header.Get("content-length"),
                header.Get("content-type"))
        copyHeader(w.Header(), http.Header(header))
        w.WriteHeader(statusCode)
        buf.WriteTo(w)
        return
}

// copy http headers over
func copyHeader(dst, src http.Header) {
        for k, vv := range src {
                for _, v := range vv {
                        dst.Add(k, v)
                }
        }
}

