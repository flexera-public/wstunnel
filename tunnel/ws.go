// Copyright (c) 2014 RightScale, Inc. - see LICENSE

package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/inconshreveable/log15.v2"
)

var _ fmt.Formatter

func httpError(log log15.Logger, w http.ResponseWriter, token, err string, code int) {
	log.Info("ERR", "token", token, "status", code, "err", err)
	http.Error(w, err, code)
}

const (
	WS_read_close  = iota
	WS_read_error  = iota
	WS_write_error = iota
)

func wsp(ws *websocket.Conn) string { return fmt.Sprintf("%p", ws) }

// Handler for websockets tunnel establishment requests
func wsHandler(t *WSTunnelServer, w http.ResponseWriter, r *http.Request) {
	addr := r.Header.Get("X-Forwarded-For")
	if addr == "" {
		addr = r.RemoteAddr
	}
	// Verify that an origin header with a token is provided
	tok := r.Header.Get("Origin")
	if tok == "" {
		httpError(t.Log, w, addr, "Origin header with rendez-vous token required", 400)
		return
	}
	if len(tok) < MIN_TOKEN_LEN {
		httpError(t.Log, w, addr,
			fmt.Sprintf("Rendez-vous token (%s) is too short (must be %d chars)",
				tok, MIN_TOKEN_LEN), 400)
		return
	}
	logTok := cutToken(token(tok))
	// Upgrade to web sockets
	ws, err := websocket.Upgrade(w, r, nil, 1000*1024, 1000*1024)
	if _, ok := err.(websocket.HandshakeError); ok {
		t.Log.Info("WS new tunnel connection rejected", "token", logTok, "addr", addr,
			"err", "Not a websocket handshake")
		httpError(t.Log, w, logTok, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		t.Log.Info("WS new tunnel connection rejected", "token", logTok, "addr", addr,
			"err", err.Error())
		httpError(t.Log, w, logTok, err.Error(), 400)
		return
	}
	// Get/Create RemoteServer
	rs := t.getRemoteServer(token(tok), true)
	rs.remoteAddr = addr
	rs.lastActivity = time.Now()
	t.Log.Info("WS new tunnel connection", "token", logTok, "addr", addr, "ws", wsp(ws),
		"rs", rs)
	// do reverse DNS lookup asynchronously
	go func() {
		rs.remoteName, rs.remoteWhois = ipAddrLookup(t.Log, rs.remoteAddr)
	}()
	// Set safety limits
	ws.SetReadLimit(1000 * 1024 * 1024)
	// Start timeout handling
	wsSetPingHandler(t, ws, rs)
	// Create synchronization channel
	ch := make(chan int, 2)
	// Spawn goroutine to read responses
	go wsReader(rs, ws, t.WSTimeout, ch)
	// Send requests
	wsWriter(rs, ws, ch)
}

func wsSetPingHandler(t *WSTunnelServer, ws *websocket.Conn, rs *remoteServer) {
	// timeout handler sends a close message, waits a few seconds, then kills the socket
	timeout := func() {
		ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(1*time.Second))
		time.Sleep(5 * time.Second)
		rs.log.Info("WS closing due to ping timeout", "ws", wsp(ws))
		ws.Close()
	}
	// timeout timer
	timer := time.AfterFunc(t.WSTimeout, timeout)
	// ping handler resets last ping time
	ph := func(message string) error {
		timer.Reset(t.WSTimeout)
		ws.WriteControl(websocket.PongMessage, []byte(message), time.Now().Add(t.WSTimeout/3))
		// update lastActivity
		rs.lastActivity = time.Now()
		return nil
	}
	ws.SetPingHandler(ph)
}

// Pick requests off the RemoteServer queue and send them into the tunnel
func wsWriter(rs *remoteServer, ws *websocket.Conn, ch chan int) {
	var req *remoteRequest
	var err error
	for {
		// fetch a request
		select {
		case req = <-rs.requestQueue:
			// awesome...
		case _ = <-ch:
			// time to close shop
			rs.log.Info("WS closing on signal", "ws", wsp(ws))
			ws.Close()
			return
		}
		//log.Printf("WS->%s#%d start %s", req.token, req.id, req.info)
		// See whether the request has already expired
		if req.deadline.Before(time.Now()) {
			req.replyChan <- responseBuffer{
				err: errors.New("Timeout before forwarding the request"),
			}
			req.log.Info("WS   SND timeout before sending", "ago",
				time.Now().Sub(req.deadline).Seconds())
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
		req.log.Info("WS   SND", "info", req.info)
	}
	// tell the sender to retry the request
	req.replyChan <- responseBuffer{err: RetryError}
	req.log.Info("WS error causes retry")
	// close up shop
	ws.WriteControl(websocket.CloseMessage, nil, time.Now().Add(5*time.Second))
	time.Sleep(2 * time.Second)
	ws.Close()
}

// Read responses from the tunnel and fulfill pending requests
func wsReader(rs *remoteServer, ws *websocket.Conn, wsTimeout time.Duration, ch chan int) {
	var err error
	log_token := cutToken(rs.token)
	// continue reading until we get an error
	for {
		ws.SetReadDeadline(time.Time{}) // no timeout, there's the ping-pong for that
		// read a message from the tunnel
		var t int
		var r io.Reader
		t, r, err = ws.NextReader()
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
		// read request itself, the size is limited by the SetReadLimit on the websocket
		var buf []byte
		buf, err = ioutil.ReadAll(r)
		if err != nil {
			break
		}
		rs.log.Info("WS   RCV", "id", id, "ws", wsp(ws), "len", len(buf))
		// try to match request
		rs.requestSetMutex.Lock()
		req := rs.requestSet[id]
		rs.lastActivity = time.Now()
		rs.requestSetMutex.Unlock()
		// let's see...
		if req != nil {
			rb := responseBuffer{response: bytes.NewBuffer(buf)}
			// try to enqueue response
			select {
			case req.replyChan <- rb:
				// great!
			default:
				rs.log.Info("WS   RCV can't enqueue response", "id", id, "ws", wsp(ws))
			}
		} else {
			rs.log.Info("%s #%d: WS   RCV orphan response", "id", id, "ws", wsp(ws))
		}
	}
	// print error message
	if err != nil {
		rs.log.Info("WS   closing", "token", log_token, "err", err.Error(), "ws", wsp(ws))
	}
	// close up shop
	ch <- 0 // notify sender
	time.Sleep(2 * time.Second)
	ws.Close()
}
