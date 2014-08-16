// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"time"
)

var _ fmt.Formatter

func httpError(w http.ResponseWriter, tok, str string, code int) {
	log.Printf("%s: WS   ERR status=%d: %s\n", code, str)
	http.Error(w, str, code)
}

const (
	WS_read_close  = iota
	WS_read_error  = iota
	WS_write_error = iota
)

// Handler for websockets tunnel establishment requests
func wsHandler(w http.ResponseWriter, r *http.Request) {
	addr := r.Header.Get("X-Forwarded-For")
	if addr == "" {
		addr = r.RemoteAddr
	}
	// Verify that an origin header with a token is provided
	token := r.Header.Get("Origin")
	if token == "" {
		httpError(w, addr, "Origin header with rendez-vous token required", 400)
		return
	}
	if len(token) < *tokLen {
		httpError(w, addr,
			fmt.Sprintf("Rendez-vous token (%s) is too short (must be %d chars)",
				token, *tokLen), 400)
		return
	}
	logTok := CutToken(Token(token))
	log.Printf("%s: WS connection from %s", logTok, addr)
	// Upgrade to web sockets
	ws, err := websocket.Upgrade(w, r, nil, 100*1024, 100*1024)
	if _, ok := err.(websocket.HandshakeError); ok {
		httpError(w, logTok, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		httpError(w, logTok, err.Error(), 400)
		return
	}
	// Get/Create RemoteServer
	rs := GetRemoteServer(Token(token))
	rs.remoteAddr = addr
	rs.lastActivity = time.Now()
	// do reverse DNS lookup asynchronously
	go func() {
		rs.remoteName, rs.remoteWhois = ipAddrLookup(rs.remoteAddr)
	}()
	// Set safety limits
	ws.SetReadLimit(100 * 1024 * 1024)
	// Start timout handling
	wsSetPingHandler(ws, rs)
	// Create synchronization channel
	ch := make(chan int, 2)
	// Spawn goroutine to read responses
	go wsReader(rs, ws, ch)
	// Send requests
	wsWriter(rs, ws, ch)
}

func wsSetPingHandler(ws *websocket.Conn, rs *RemoteServer) {
	// timeout handler sends a close message, waits a few seconds, then kills the socket
	timeout := func() {
		ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(1*time.Second))
		time.Sleep(5 * time.Second)
		ws.Close()
	}
	// timeout timer
	timer := time.AfterFunc(wsTimeout, timeout)
	// ping handler resets last ping time
	ph := func(message string) error {
		timer.Reset(wsTimeout)
		ws.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(wsTimeout/3))
		// update lastActivity
		rs.lastActivity = time.Now()
		return nil
	}
	ws.SetPingHandler(ph)
}

// Pick requests off the RemoteServer queue and send them into the tunnel
func wsWriter(rs *RemoteServer, ws *websocket.Conn, ch chan int) {
	var req *RemoteRequest
	var err error
	log_token := CutToken(rs.token)
	for {
		// fetch a request
		select {
		case req = <-rs.requestQueue:
			// awesome...
		case _ = <-ch:
			// time to close shop
			log.Printf("%s: WS closing on signal", log_token)
			ws.Close()
			return
		}
		//log.Printf("WS->%s#%d start %s\n", req.token, req.id, req.info)
		// See whether the request has already expired
		if req.deadline.Before(time.Now()) {
			req.replyChan <- ResponseBuffer{
				err: errors.New("Timeout before forwarding the request"),
			}
			log.Printf("%s #%d: WS  SND timeout before sending (%.0fsecs ago)",
				log_token, req.id, time.Now().Sub(req.deadline).Seconds())
			continue
		}
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
		log.Printf("%s #%d: WS   SND %s\n", log_token, req.id, req.info)
	}
	// tell the sender to retry the request
	req.replyChan <- ResponseBuffer{err: RetryError}
	log.Printf("%s #%d: WS causes retry\n", log_token, req.id)
	// close up shop
	ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(5*time.Second))
	time.Sleep(2 * time.Second)
	ws.Close()
}

// Read responses from the tunnel and fulfill pending requests
func wsReader(rs *RemoteServer, ws *websocket.Conn, ch chan int) {
	var err error
	log_token := CutToken(rs.token)
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
		// read request itself
		buf, err := ioutil.ReadAll(r)
		if err != nil {
			break
		}
		log.Printf("%s #%d: WS   RCV", log_token, id)
		// try to match request
		rs.requestSetMutex.Lock()
		req := rs.requestSet[id]
		rs.lastActivity = time.Now()
		rs.requestSetMutex.Unlock()
		// let's see...
		if req != nil {
			rb := ResponseBuffer{response: bytes.NewBuffer(buf)}
			// try to enqueue response
			select {
			case req.replyChan <- rb:
				// great!
			default:
				log.Printf("%s #%d: WS   RCV can't enqueue response\n", log_token, id)
			}
		} else {
			log.Printf("%s #%d: WS   RCV orphan response\n", log_token, id)
		}
	}
	// print error message
	if err != nil {
		log.Printf("%s: WS   closing due to %s\n", log_token, err.Error())
	}
	// close up shop
	ch <- 0 // notify sender
	time.Sleep(2 * time.Second)
	ws.Close()
}
