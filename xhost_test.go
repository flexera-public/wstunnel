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

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Testing xhost requests", func() {

	var server *ghttp.Server
	var cliStop, srvStop chan struct{}
	var wstunUrl string
	var wstunToken string
	var cliStart func(server, regexp string) chan struct{}

	BeforeEach(func() {
		wstunToken = "test567890123456-" + strconv.Itoa(rand.Int()%1000000)
		server = ghttp.NewServer()
		fmt.Fprintf(os.Stderr, "ghttp started on %s\n", server.URL())
		serverBasics(server)

		l, _ := net.Listen("tcp", "127.0.0.1:0")
		srvStop = wstunsrv([]string{}, l)
		fmt.Fprintf(os.Stderr, "Server started\n")
		wstunUrl = "http://" + l.Addr().String()
		cliStart = func(server, regexp string) chan struct{} {
			return wstuncli([]string{
				"-token", wstunToken, "-tunnel", "ws://" + l.Addr().String(),
				"-server", server, "-regexp", regexp,
			})
		}
	})
	AfterEach(func() {
		cliStop <- struct{}{}
		srvStop <- struct{}{}
		server.Close()
	})

	It("Respects host header", func() {
		cliStop = cliStart("http://localhost:123", `http://127\.0\.0\.[0-9]:[0-9]+`)
		server.AppendHandlers(
			ghttp.CombineHandlers(
				ghttp.VerifyRequest("GET", "/hello"),
				//ghttp.VerifyHeaderKV("Host", server.URL()),
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
		cliStop = cliStart("http://localhost:123", `http://127\.0\.0\.[0-9]:[0-9]+`)
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
		Ω(string(respBody)).Should(Equal("X-Host header does not match regexp"))
		Ω(resp.StatusCode).Should(Equal(403))
	})

	It("Handles the default server", func() {
		cliStop = cliStart(server.URL(), `xxx`)
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
