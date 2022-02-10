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

package tunnel

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"os"
	"regexp"
	"runtime"

	//"crypto/tls"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"

	// imported per documentation - https://golang.org/pkg/net/http/pprof/
	_ "net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/inconshreveable/log15.v2"
)

var _ fmt.Formatter

// WSTunnelClient represents a persistent tunnel that can cycle through many websockets. The
// fields in this struct are relatively static/constant. The conn field points to the latest
// websocket, but it's important to realize that there may be goroutines handling older
// websockets that are not fully closed yet running at any point in time
type WSTunnelClient struct {
	Token          string         // Rendez-vous token
	Tunnel         *url.URL       // websocket server to connect to (ws[s]://hostname:port)
	Server         string         // local HTTP(S) server to send received requests to (default server)
	InternalServer http.Handler   // internal Server to dispatch HTTP requests to
	Regexp         *regexp.Regexp // regexp for allowed local HTTP(S) servers
	Insecure       bool           // accept self-signed SSL certs from local HTTPS servers
	Cert           string         // accept provided certificate from local HTTPS servers
	Timeout        time.Duration  // timeout on websocket
	Proxy          *url.URL       // if non-nil, external proxy to use
	Log            log15.Logger   // logger with "pkg=WStuncli"
	StatusFd       *os.File       // output periodic tunnel status information
	Connected      bool           // true when we have an active connection to wstunsrv
	exitChan       chan struct{}  // channel to tell the tunnel goroutines to end
	conn           *WSConnection
	ClientPorts    []int // array of ports for client to listen on.
	//ws             *websocket.Conn // websocket connection
}

// WSConnection represents a single websocket connection
type WSConnection struct {
	Log log15.Logger    // logger with "ws=0x1234"
	ws  *websocket.Conn // websocket connection
	tun *WSTunnelClient // link back to tunnel
}

var httpClient http.Client // client used for all requests, gets special transport for -insecure

//===== Main =====

//NewWSTunnelClient Creates a new WSTunnelClient from command line
func NewWSTunnelClient(args []string) *WSTunnelClient {
	wstunCli := WSTunnelClient{}

	var cliFlag = flag.NewFlagSet("client", flag.ExitOnError)
	cliFlag.StringVar(&wstunCli.Token, "token", "",
		"rendez-vous token identifying this server")
	var tunnel = cliFlag.String("tunnel", "",
		"websocket server ws[s]://user:pass@hostname:port to connect to")
	cliFlag.StringVar(&wstunCli.Server, "server", "",
		"http server http[s]://hostname:port to send received requests to")
	cliFlag.BoolVar(&wstunCli.Insecure, "insecure", false,
		"accept self-signed SSL certs from local HTTPS servers")
	var sre = cliFlag.String("regexp", "",
		"regexp for local HTTP(S) server to allow sending received requests to")
	var tout = cliFlag.Int("timeout", 30, "timeout on websocket in seconds")
	var pidf = cliFlag.String("pidfile", "", "path for pidfile")
	var logf = cliFlag.String("logfile", "", "path for log file")
	var statf = cliFlag.String("statusfile", "", "path for status file")
	var proxy = cliFlag.String("proxy", "",
		"use HTTPS proxy http://user:pass@hostname:port")
	var cliports = cliFlag.String("client-ports", "",
		"comma separated list of client listening ports ex: -client-ports 8000..8100,8300..8400,8500,8505")
	cliFlag.StringVar(&wstunCli.Cert, "certfile", "", "path for trusted certificate in PEM-encoded format")

	cliFlag.Parse(args)

	wstunCli.Log = makeLogger("WStuncli", *logf, "")
	writePid(*pidf)
	wstunCli.Timeout = calcWsTimeout(*tout)

	// process -statusfile
	if *statf != "" {
		fd, err := os.Create(*statf)
		if err != nil {
			log15.Crit("Can't create statusfile", "err", err.Error())
			os.Exit(1)
		}
		wstunCli.StatusFd = fd
	}

	// process -regexp
	if *sre != "" {
		var err error
		wstunCli.Regexp, err = regexp.Compile(*sre)
		if err != nil {
			log15.Crit("Can't parse -regexp", "err", err.Error())
			os.Exit(1)
		}
	}

	if *tunnel != "" {
		tunnelUrl, err := url.Parse(*tunnel)
		if err != nil {
			log15.Crit(fmt.Sprintf("Invalid tunnel address: %q, %v", *tunnel, err))
			os.Exit(1)
		}

		if tunnelUrl.Scheme != "ws" && tunnelUrl.Scheme != "wss" {
			log15.Crit(fmt.Sprintf("Remote tunnel (-tunnel option) must begin with ws:// or wss://"))
			os.Exit(1)
		}

		wstunCli.Tunnel = tunnelUrl
	} else {
		log15.Crit(fmt.Sprintf("Must specify tunnel server ws://hostname:port using -tunnel option"))
		os.Exit(1)
	}

	// process -proxy or look for standard unix env variables
	if *proxy == "" {
		envNames := []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"}
		for _, n := range envNames {
			if p := os.Getenv(n); p != "" {
				*proxy = p
				break
			}
		}
	}
	if *proxy != "" {
		proxyURL, err := url.Parse(*proxy)
		if err != nil || !strings.HasPrefix(proxyURL.Scheme, "http") {
			// proxy was bogus. Try prepending "http://" to it and
			// see if that parses correctly. If not, we fall
			// through and complain about the original one.
			if proxyURL, err = url.Parse("http://" + *proxy); err != nil {
				log15.Crit(fmt.Sprintf("Invalid proxy address: %q, %v", *proxy, err))
				os.Exit(1)
			}
		}

		wstunCli.Proxy = proxyURL
	}

	if *cliports != "" {
		portList := strings.Split(*cliports, ",")
		clientPorts := []int{}
		log15.Info("Attempting to start client with ports: " + *cliports)

		for _, v := range portList {
			if strings.Contains(v, "..") {
				k := strings.Split(v, "..")
				bInt, err := strconv.Atoi(k[0])
				if err != nil {
					log15.Crit(fmt.Sprintf("Invalid Port Assignment: %q in range: %q", k[0], v))
					os.Exit(1)
				}

				eInt, err := strconv.Atoi(k[1])
				if err != nil {
					log15.Crit(fmt.Sprintf("Invalid Port Assignment: %q in range: %q", k[1], v))
					os.Exit(1)
				}

				if eInt < bInt {
					log15.Crit(fmt.Sprintf("End port %d can not be less than beginning port %d", eInt, bInt))
					os.Exit(1)
				}

				for n := bInt; n <= eInt; n++ {
					intPort := n
					clientPorts = append(clientPorts, intPort)
				}
			} else {
				intPort, err := strconv.Atoi(v)
				if err != nil {
					log15.Crit(fmt.Sprintf("Can not convert %q to integer", v))
					os.Exit(1)
				}
				clientPorts = append(clientPorts, intPort)
			}
		}
		wstunCli.ClientPorts = clientPorts
	}

	return &wstunCli
}

//Start creates the wstunnel connection.
func (t *WSTunnelClient) Start() error {
	t.Log.Info(VV)

	// validate -server
	if t.InternalServer != nil {
		t.Server = ""
	} else if t.Server != "" {
		if !strings.HasPrefix(t.Server, "http://") && !strings.HasPrefix(t.Server, "https://") {
			return fmt.Errorf("Local server (-server option) must begin with http:// or https://")
		}
		t.Server = strings.TrimSuffix(t.Server, "/")
	}

	// validate token and timeout
	if t.Token == "" {
		return fmt.Errorf("Must specify rendez-vous token using -token option")
	}

	tlsClientConfig := tls.Config{}
	if t.Insecure {
		t.Log.Info("Accepting unverified SSL certs from local HTTPS servers")
		tlsClientConfig.InsecureSkipVerify = t.Insecure
	}
	if t.Cert != "" {
		// Get the SystemCertPool, continue with an empty pool on error
		rootCAs, _ := x509.SystemCertPool()
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		// Read in the cert file
		certs, err := ioutil.ReadFile(t.Cert)
		if err != nil {
			return fmt.Errorf("Failed to read certificate file %q: %v", t.Cert, err)
		}
		// Append our cert to the system pool
		if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
			return fmt.Errorf("Failed to appended certificate file %q to pool: %v", t.Cert, err)
		}
		t.Log.Info("Explicitly accepting provided SSL certificate")
		tlsClientConfig.RootCAs = rootCAs
	}
	httpClient = http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tlsClientConfig,
		},
	}

	if t.InternalServer != nil {
		t.Log.Info("Dispatching to internal server")
	} else if t.Server != "" || t.Regexp != nil {
		t.Log.Info("Dispatching to external server(s)", "server", t.Server, "regexp", t.Regexp)
	} else {
		return fmt.Errorf("Must specify internal server or server or regexp")
	}

	if t.Proxy != nil {
		username := "(none)"
		if u := t.Proxy.User; u != nil {
			username = u.Username()
		}
		t.Log.Info("Using HTTPS proxy", "url", t.Proxy.Host, "user", username)
	}

	// for test purposes we have a signal that tells wstuncli to exit instead of reopening
	// a fresh connection.
	t.exitChan = make(chan struct{}, 1)

	//===== Goroutine =====

	// Keep opening websocket connections to tunnel requests
	go func() {
		for {
			d := &websocket.Dialer{
				NetDial:         t.wsProxyDialer,
				ReadBufferSize:  100 * 1024,
				WriteBufferSize: 100 * 1024,
				TLSClientConfig: &tlsClientConfig,
			}
			h := make(http.Header)
			h.Add("Origin", t.Token)
			if auth := proxyAuth(t.Tunnel); auth != "" {
				h.Add("Authorization", auth)
			}
			url := fmt.Sprintf("%s://%s/_tunnel", t.Tunnel.Scheme, t.Tunnel.Host)
			timer := time.NewTimer(10 * time.Second)
			t.Log.Info("WS   Opening", "url", url, "token", t.Token[0:5]+"...")
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
				t.Log.Error("Error opening connection",
					"err", err.Error(), "info", extra)
			} else {
				t.conn = &WSConnection{ws: ws, tun: t,
					Log: t.Log.New("ws", fmt.Sprintf("%p", ws))}
				// Safety setting
				ws.SetReadLimit(100 * 1024 * 1024)
				// Request Loop
				srv := t.Server
				if t.InternalServer != nil {
					srv = "<internal>"
				}
				t.conn.Log.Info("WS   ready", "server", srv)
				t.Connected = true
				t.conn.handleRequests()
				t.Connected = false
			}
			// check whether we need to exit
			exitLoop := false
			select {
			case <-t.exitChan:
				exitLoop = true
			default: // non-blocking receive
			}
			if exitLoop {
				break
			}

			<-timer.C // ensure we don't open connections too rapidly
		}
	}()

	return nil
}

//Stop closes the wstunnel channel
func (t *WSTunnelClient) Stop() {
	t.exitChan <- struct{}{}
	if t.conn != nil && t.conn.ws != nil {
		t.conn.ws.Close()
	}
}

// Main function to handle WS requests: it reads a request from the socket, then forks
// a goroutine to perform the actual http request and return the result
func (wsc *WSConnection) handleRequests() {
	go wsc.pinger()
	for {
		wsc.ws.SetReadDeadline(time.Time{}) // separate ping-pong routine does timeout
		typ, r, err := wsc.ws.NextReader()
		if err != nil {
			wsc.Log.Info("WS   ReadMessage", "err", err.Error())
			break
		}
		if typ != websocket.BinaryMessage {
			wsc.Log.Warn("WS   invalid message type", "type", typ)
			break
		}
		// give the sender a minute to produce the request
		wsc.ws.SetReadDeadline(time.Now().Add(time.Minute))
		// read request id
		var id int16
		_, err = fmt.Fscanf(io.LimitReader(r, 4), "%04x", &id)
		if err != nil {
			wsc.Log.Warn("WS   cannot read request ID", "err", err.Error())
			break
		}
		// read the whole message, this is bounded (to something large) by the
		// SetReadLimit on the websocket. We have to do this because we want to handle
		// the request in a goroutine (see "go finish..Request" calls below) and the
		// websocket doesn't allow us to have multiple goroutines reading...
		buf, err := ioutil.ReadAll(r)
		if err != nil {
			wsc.Log.Warn("WS   cannot read request message", "id", id, "err", err.Error())
			break
		}
		if len(buf) > 1024*1024 {
			wsc.Log.Info("WS   long message", "len", len(buf))
		}
		wsc.Log.Debug("WS   message", "len", len(buf))
		r = bytes.NewReader(buf)
		// read request itself
		req, err := http.ReadRequest(bufio.NewReader(r))
		if err != nil {
			wsc.Log.Warn("WS   cannot read request body", "id", id, "err", err.Error())
			break
		}
		// Hand off to goroutine to finish off while we read the next request
		if wsc.tun.InternalServer != nil {
			go wsc.finishInternalRequest(id, req)
		} else {
			go wsc.finishRequest(id, req)
		}
	}
	// delay a few seconds to allow for writes to drain and then force-close the socket
	go func() {
		time.Sleep(5 * time.Second)
		wsc.ws.Close()
	}()
}

//===== Keep-alive ping-pong =====

// Pinger that keeps connections alive and terminates them if they seem stuck
func (wsc *WSConnection) pinger() {
	defer func() {
		// panics may occur in WriteControl (in unit tests at least) for closed
		// websocket connections
		if x := recover(); x != nil {
			wsc.Log.Error("Panic in pinger", "err", x)
		}
	}()
	wsc.Log.Info("pinger starting")
	tunTimeout := wsc.tun.Timeout

	// timeout handler sends a close message, waits a few seconds, then kills the socket
	timeout := func() {
		if wsc.ws == nil {
			return
		}
		wsc.ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(1*time.Second))
		wsc.Log.Info("ping timeout, closing WS")
		time.Sleep(5 * time.Second)
		if wsc.ws != nil {
			wsc.ws.Close()
		}
	}
	// timeout timer
	timer := time.AfterFunc(tunTimeout, timeout)
	// pong handler resets last pong time
	ph := func(message string) error {
		timer.Reset(tunTimeout)
		if sf := wsc.tun.StatusFd; sf != nil {
			sf.Seek(0, 0)
			wsc.writeStatus()
			pos, _ := sf.Seek(0, 1)
			sf.Truncate(pos)
		}
		return nil
	}
	wsc.ws.SetPongHandler(ph)
	// ping loop, ends when socket is closed...
	for {
		if wsc.ws == nil {
			break
		}
		err := wsc.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(tunTimeout/3))
		if err != nil {
			break
		}
		time.Sleep(tunTimeout / 3)
	}
	wsc.Log.Info("pinger ending (WS errored or closed)")
	wsc.ws.Close()
}

func (wsc *WSConnection) writeStatus() {
	fmt.Fprintf(wsc.tun.StatusFd, "Unix: %d\n", time.Now().Unix())
	fmt.Fprintf(wsc.tun.StatusFd, "Time: %s\n", time.Now().UTC().Format(time.RFC3339))
}

func (t *WSTunnelClient) wsDialerLocalPort(network string, addr string, ports []int) (conn net.Conn, err error) {
	for _, port := range ports {
		client, err := net.ResolveTCPAddr("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return nil, err
		}

		server, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			return nil, err
		}

		conn, err = net.DialTCP(network, client, server)
		if (conn != nil) && (err == nil) {
			return conn, nil
		}
		err = fmt.Errorf("WS: error connecting with local port %d: %s", port, err.Error())
		t.Log.Info(err.Error())
	}

	err = errors.New("WS: Could not connect using any of the ports in range: " + fmt.Sprint(ports))
	return nil, err
}

//===== Proxy support =====
// Bits of this taken from golangs net/http/transport.go. Gorilla websocket lib
// allows you to pass in a custom net.Dial function, which it will call instead
// of net.Dial. net.Dial normally just opens up a tcp socket for you. We go one
// extra step and issue an HTTP CONNECT command after the socket is open. After
// HTTP CONNECT is issued and successful, we hand the reins back to gorilla,
// which will then set up SSL and handle the websocket UPGRADE request.
// Note this only handles HTTPS connections through the proxy. HTTP requires
// header rewriting.
func (t *WSTunnelClient) wsProxyDialer(network string, addr string) (conn net.Conn, err error) {
	if t.Proxy == nil {
		if len(t.ClientPorts) != 0 {
			conn, err = t.wsDialerLocalPort(network, addr, t.ClientPorts)
			return conn, err
		}
		return net.Dial(network, addr)
	}

	conn, err = net.Dial("tcp", t.Proxy.Host)
	if err != nil {
		err = fmt.Errorf("WS: error connecting to proxy %s: %s", t.Proxy.Host, err.Error())
		return nil, err
	}

	pa := proxyAuth(t.Proxy)

	connectReq := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	if pa != "" {
		connectReq.Header.Set("Proxy-Authorization", pa)
	}
	connectReq.Write(conn)

	// Read and parse CONNECT response.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != 200 {
		//body, _ := ioutil.ReadAll(io.LimitReader(resp.Body, 500))
		//resp.Body.Close()
		//return nil, errors.New("proxy refused connection" + string(body))
		f := strings.SplitN(resp.Status, " ", 2)
		conn.Close()
		return nil, fmt.Errorf(f[1])
	}
	return conn, nil
}

// proxyAuth returns the Proxy-Authorization header to set
// on requests, if applicable.
func proxyAuth(proxy *url.URL) string {
	if u := proxy.User; u != nil {
		username := u.Username()
		password, _ := u.Password()
		return "Basic " + basicAuth(username, password)
	}
	return ""
}

// See 2 (end of page 4) http://www.ietf.org/rfc/rfc2617.txt
func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
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

//===== HTTP response writer, used for internal request handlers

type responseWriter struct {
	resp *http.Response
	buf  *bytes.Buffer
}

func newResponseWriter(req *http.Request) *responseWriter {
	buf := bytes.Buffer{}
	resp := http.Response{
		Header:        make(http.Header),
		Body:          ioutil.NopCloser(&buf),
		StatusCode:    -1,
		ContentLength: -1,
		Proto:         req.Proto,
		ProtoMajor:    req.ProtoMajor,
		ProtoMinor:    req.ProtoMinor,
	}
	return &responseWriter{
		resp: &resp,
		buf:  &buf,
	}

}

func (rw *responseWriter) Write(buf []byte) (int, error) {
	if rw.resp.StatusCode == -1 {
		rw.WriteHeader(200)
	}
	return rw.buf.Write(buf)
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.resp.StatusCode = code
	rw.resp.Status = http.StatusText(code)
}

func (rw *responseWriter) Header() http.Header { return rw.resp.Header }

func (rw *responseWriter) finishResponse() error {
	if rw.resp.StatusCode == -1 {
		return fmt.Errorf("HTTP internal handler did not call Write or WriteHeader")
	}
	rw.resp.ContentLength = int64(rw.buf.Len())

	return nil
}

//===== HTTP driver and response sender =====

var wsWriterMutex sync.Mutex // mutex to allow a single goroutine to send a response at a time

// Issue a request to an internal handler. This duplicates some logic found in
// net.http.serve http://golang.org/src/net/http/server.go?#L1124 and
// net.http.readRequest http://golang.org/src/net/http/server.go?#L
func (wsc *WSConnection) finishInternalRequest(id int16, req *http.Request) {
	log := wsc.Log.New("id", id, "verb", req.Method, "uri", req.RequestURI)
	log.Info("HTTP issuing internal request")

	// Remove hop-by-hop headers
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}

	// Add fake protocol version
	req.Proto = "HTTP/1.0"
	req.ProtoMajor = 1
	req.ProtoMinor = 0

	// Dump the request into a buffer in case we want to log it
	dump, _ := httputil.DumpRequest(req, false)
	log.Debug("dump", "req", strings.Replace(string(dump), "\r\n", " || ", -1))

	// Make sure we don't die if a panic occurs in the handler
	defer func() {
		if err := recover(); err != nil {
			const size = 64 << 10
			buf := make([]byte, size)
			buf = buf[:runtime.Stack(buf, false)]
			log.Error("HTTP panic in handler", "err", err, "stack", string(buf))
		}
	}()

	// Concoct Response
	rw := newResponseWriter(req)

	// Issue the request to the HTTP server
	wsc.tun.InternalServer.ServeHTTP(rw, req)

	err := rw.finishResponse()
	if err != nil {
		//dump2, _ := httputil.DumpResponse(resp, true)
		//log15.Info("handleWsRequests: request error", "err", err.Error(),
		//	"req", string(dump), "resp", string(dump2))
		log.Info("HTTP request error", "err", err.Error())
		wsc.writeResponseMessage(id, concoctResponse(req, err.Error(), 502))
		return
	}

	log.Info("HTTP responded", "status", rw.resp.StatusCode)
	wsc.writeResponseMessage(id, rw.resp)
}

func (wsc *WSConnection) finishRequest(id int16, req *http.Request) {

	log := wsc.Log.New("id", id, "verb", req.Method, "uri", req.RequestURI)

	// Honor X-Host header
	host := wsc.tun.Server
	xHost := req.Header.Get("X-Host")
	if xHost != "" {
		re := wsc.tun.Regexp
		if re == nil {
			log.Info("WS   got x-host header but no regexp provided")
			wsc.writeResponseMessage(id, concoctResponse(req,
				"X-Host header disallowed by wstunnel cli (no -regexp option)", 403))
			return
		} else if re.FindString(xHost) == xHost {
			host = xHost
		} else {
			log.Info("WS   x-host disallowed by regexp", "x-host", xHost, "regexp",
				re.String(), "match", re.FindString(xHost))
			wsc.writeResponseMessage(id, concoctResponse(req,
				"X-Host header '"+xHost+"' does not match regexp in wstunnel cli",
				403))
			return
		}
	} else if host == "" {
		log.Info("WS   no x-host header and -server not specified")
		wsc.writeResponseMessage(id, concoctResponse(req,
			"X-Host header required by wstunnel cli (no -server option)", 403))
		return
	}
	req.Header.Del("X-Host")

	// Construct the URL for the outgoing request
	var err error
	req.URL, err = url.Parse(fmt.Sprintf("%s%s", host, req.RequestURI))
	if err != nil {
		log.Warn("WS   cannot parse requestURI", "err", err.Error())
		wsc.writeResponseMessage(id, concoctResponse(req, "Cannot parse request URI", 400))
		return
	}
	req.Host = req.URL.Host // we delete req.Header["Host"] further down
	req.RequestURI = ""
	log.Info("HTTP issuing request", "url", req.URL.String())

	// Remove hop-by-hop headers
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	// Issue the request to the HTTP server
	dump, err := httputil.DumpRequest(req, false)
	log.Debug("dump", "req", strings.Replace(string(dump), "\r\n", " || ", -1))
	if err != nil {
		log.Warn("error dumping request", "err", err.Error())
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		//dump2, _ := httputil.DumpResponse(resp, true)
		//log15.Info("handleWsRequests: request error", "err", err.Error(),
		//	"req", strings.Replace(string(dump), "\r\n", " || ", -1))
		log.Info("HTTP request error", "err", err.Error())
		wsc.writeResponseMessage(id, concoctResponse(req, err.Error(), 502))
		return
	}
	log.Info("HTTP responded", "status", resp.Status)
	defer resp.Body.Close()

	wsc.writeResponseMessage(id, resp)
}

// Write the response message to the websocket
func (wsc *WSConnection) writeResponseMessage(id int16, resp *http.Response) {
	// Get writer's lock
	wsWriterMutex.Lock()
	defer wsWriterMutex.Unlock()
	// Write response into the tunnel
	wsc.ws.SetWriteDeadline(time.Now().Add(time.Minute))
	w, err := wsc.ws.NextWriter(websocket.BinaryMessage)
	// got an error, reply with a "hey, retry" to the request handler
	if err != nil {
		wsc.Log.Warn("WS   NextWriter", "err", err.Error())
		wsc.ws.Close()
		return
	}

	// write the request Id
	_, err = fmt.Fprintf(w, "%04x", id)
	if err != nil {
		wsc.Log.Warn("WS   cannot write request Id", "err", err.Error())
		wsc.ws.Close()
		return
	}

	// write the response itself
	err = resp.Write(w)
	if err != nil {
		wsc.Log.Warn("WS   cannot write response", "err", err.Error())
		wsc.ws.Close()
		return
	}

	// done
	err = w.Close()
	if err != nil {
		wsc.Log.Warn("WS   write-close failed", "err", err.Error())
		wsc.ws.Close()
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
