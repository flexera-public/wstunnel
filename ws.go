// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
        "bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
        "net/http"
	"time"
        "github.com/gorilla/websocket"
        _ "net/http/pprof"
)

var _ fmt.Formatter

func httpError(w http.ResponseWriter, str string, code int) {
        log.Printf("WS ERR %d: %s\n", code, str)
        http.Error(w, str, code)
}

const (
        WS_read_close = iota
        WS_read_error = iota
        WS_write_error = iota
)

// Handler for websockets tunnel establishment requests
func wsHandler(w http.ResponseWriter, r *http.Request) {
        // Verify that an origin header with a token is provided
        token := r.Header.Get("Origin")
        if token == "" {
                httpError(w, fmt.Sprintf("Origin header with rendez-vous token required (%s)",
                        r.RemoteAddr), 400)
                return
        }
        log.Printf("WS connection from %s (%s)", token, r.RemoteAddr)
        // Upgrade to web sockets
        ws, err := websocket.Upgrade(w, r, nil, 100*1024, 100*1024)
        if _, ok := err.(websocket.HandshakeError); ok {
                httpError(w, "Not a websocket handshake", 400)
                return
        } else if err != nil {
                httpError(w, err.Error(), 400)
                return
        }
        // Set safety limits
        ws.SetReadLimit(100*1024*1024)
        // Start timout handling
        wsSetPingHandler(ws)
        // Get/Create RemoteServer
        rs := GetRemoteServer(Token(token))
        // Create synchronization channel
        ch := make(chan int, 2)
        // Spawn goroutine to read responses
        go wsReader(rs, ws, ch)
        // Send requests
        wsWriter(rs, ws, ch)
}

func wsSetPingHandler(ws *websocket.Conn) {
        // timeout handler sends a close message, waits a few seconds, then kills the socket
        timeout := func() {
                ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(1*time.Second))
                time.Sleep(5*time.Second)
                ws.Close()
        }
        // timeout timer
        timer := time.AfterFunc(wsTimeout, timeout)
        // ping handler resets last ping time
        ph := func(message string) error {
                timer.Reset(wsTimeout)
                ws.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(wsTimeout/3))
                return nil
        }
        ws.SetPingHandler(ph)
}

// Pick requests off the RemoteServer queue and send them into the tunnel
func wsWriter(rs *RemoteServer, ws *websocket.Conn, ch chan int) {
        var req *RemoteRequest
        var err error
        for {
                // fetch a request
                select {
                case req = <-rs.requestQueue:
                        // awesome...
                case _ = <-ch:
                        // time to close shop
                        log.Printf("WS->%s closing on signal\n", rs.token)
                        ws.Close()
                        return
                }
                //log.Printf("WS->%s#%d start %s\n", req.token, req.id, req.info)
                // write the request into the tunnel
                ws.SetWriteDeadline(time.Now().Add(time.Minute))
                var w io.WriteCloser
                w, err = ws.NextWriter(websocket.BinaryMessage)
                // got an error, reply with a "hey, retry" to the request handler
                if err != nil {
                        break
                }
                // write the request Id
                _, err = fmt.Fprintf(w, "%04x", req.id)
                if err != nil {
                        break
                }
                // write the request itself
                _, err = req.buffer.WriteTo(w)
                if err != nil {
                        break
                }
                // done
                err = w.Close()
                if err != nil {
                        break
                }
                log.Printf("WS->%s#%d %s\n", req.token, req.id, req.info)
        }
        // tell the sender to retry the request
        req.replyChan <- ResponseBuffer{ err: RetryError }
        log.Printf("WS->%s#%d retry\n", req.token, req.id)
        // close up shop
        ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(5*time.Second))
        time.Sleep(2*time.Second)
        ws.Close()
}

// Read responses from the tunnel and fulfill pending requests
func wsReader(rs *RemoteServer, ws *websocket.Conn, ch chan int) {
        var err error
        // continue reading until we get an error
        for {
                ws.SetReadDeadline(time.Time{}) // no timeout, there's the ping-pong for that
                // read a message from the tunnel
                t, r, err := ws.NextReader()
                if err != nil {
                        break
                }
                if t != websocket.BinaryMessage {
                        err = fmt.Errorf("non-binary message received, type=%d", t)
                        break
                }
                // give the sender a fixed time to get us the data
                ws.SetReadDeadline(time.Now().Add(wsTimeout))
                // get request id
                var id int16
                _, err = fmt.Fscanf(io.LimitReader(r, 4), "%04x", &id)
                if err != nil {
                        break
                }
                log.Printf("WS<-%s#%d\n", rs.token, id)
                // read request itself
                buf, err := ioutil.ReadAll(r)
                if err != nil {
                        break
                }
                // try to match request
                rs.requestSetMutex.Lock()
                req := rs.requestSet[id]
                rs.requestSetMutex.Unlock()
                // let's see...
                if req != nil {
                        rb := ResponseBuffer{ response: bytes.NewBuffer(buf) }
                        // try to enqueue response
                        select {
                        case req.replyChan <- rb:
                                // great!
                        default:
                                log.Printf("WS<-%s#d: can't enqueue response\n", rs.token, id)
                        }
                } else {
                        log.Printf("WS<-%s#%d: orphan response\n", rs.token, id)
                }
        }
        // print error message
        if err != nil {
                log.Printf("WS<-%s: %s -- closing\n", rs.token, err.Error())
        }
        // close up shop
        ch <- 0 // notify sender
        time.Sleep(2*time.Second)
        ws.Close()
}
