// Copyright (c) 2015 RightScale, Inc. - see LICENSE

package main

// Omega: Alt+937

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/rightscale/wstunnel/tunnel"
)

var _ = Describe("Testing xhost requests", func() {

	var server *ghttp.Server
	var wstuncli *tunnel.WSTunnelClient
	var wstunsrv *tunnel.WSTunnelServer
	var wstunUrl string
	var wstunToken string
	var cliStart func(server, regexp string) *tunnel.WSTunnelClient

	BeforeEach(func() {
		wstunToken = "test567890123456-" + strconv.Itoa(rand.Int()%1000000)
		server = ghttp.NewServer()
		fmt.Fprintf(os.Stderr, "ghttp started on %s\n", server.URL())

		l, _ := net.Listen("tcp", "127.0.0.1:0")
		wstunsrv = tunnel.NewWSTunnelServer([]string{})
		wstunsrv.Start(l)
		fmt.Fprintf(os.Stderr, "Server started\n")
		wstunUrl = "http://" + l.Addr().String()
		cliStart = func(server, regexp string) *tunnel.WSTunnelClient {
			wstuncli = tunnel.NewWSTunnelClient([]string{
				"-token", wstunToken, "-tunnel", "ws://" + l.Addr().String(),
				"-server", server, "-regexp", regexp,
			})
			wstuncli.Start()
			return wstuncli
		}
	})
	AfterEach(func() {
		wstuncli.Stop()
		wstunsrv.Stop()
		server.Close()
	})

	It("Respects x-host header", func() {
		wstuncli = cliStart("http://localhost:123", `http://127\.0\.0\.[0-9]:[0-9]+`)
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/hello"),
				func(w http.ResponseWriter, req *http.Request) {
					Ω(req.Header.Get("Host")).Should(Equal(""))
					Ω(req.Host).Should(Equal(strings.TrimPrefix(server.URL(), "http://")))
				},
				ghttp.RespondWith(200, `HOSTED`,
					http.Header{"Content-Type": []string{"text/world"}}),
			),
		)

		req, err := http.NewRequest("GET", wstunUrl+"/_token/"+wstunToken+"/hello", nil)
		Ω(err).ShouldNot(HaveOccurred())
		req.Header.Set("X-Host", server.URL())
		resp, err := http.DefaultClient.Do(req)
		Ω(err).ShouldNot(HaveOccurred())
		respBody, err := ioutil.ReadAll(resp.Body)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(string(respBody)).Should(Equal("HOSTED"))
		Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
	})

	It("Rejects partial host regexp matches", func() {
		wstuncli = cliStart("http://localhost:123", `http://127\.0\.0\.[0-9]:[0-9]+`)
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.RespondWith(200, `HOSTED`,
					http.Header{"Content-Type": []string{"text/world"}}),
			),
		)

		req, err := http.NewRequest("GET", wstunUrl+"/_token/"+wstunToken+"/hello", nil)
		Ω(err).ShouldNot(HaveOccurred())
		req.Header.Set("X-Host", "http://google.com/"+server.URL())
		resp, err := http.DefaultClient.Do(req)
		Ω(err).ShouldNot(HaveOccurred())
		respBody, err := ioutil.ReadAll(resp.Body)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(string(respBody)).Should(ContainSubstring("X-Host header does not match regexp"))
		Ω(resp.StatusCode).Should(Equal(403))
	})

	It("Handles the default server", func() {
		wstuncli = cliStart(server.URL(), `xxx`)
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.RespondWith(200, `HOSTED`,
					http.Header{"Content-Type": []string{"text/world"}}),
			),
		)

		req, err := http.NewRequest("GET", wstunUrl+"/_token/"+wstunToken+"/hello", nil)
		Ω(err).ShouldNot(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		Ω(err).ShouldNot(HaveOccurred())
		respBody, err := ioutil.ReadAll(resp.Body)
		Ω(err).ShouldNot(HaveOccurred())
		Ω(string(respBody)).Should(Equal("HOSTED"))
		Ω(resp.Header.Get("Content-Type")).Should(Equal("text/world"))
	})

})
