# WStunnel - Web Sockets Tunnel

- Master:
[![Build Status](https://travis-ci.org/rightscale/wstunnel.svg?branch=master)](https://travis-ci.org/rightscale/wstunnel)
[![Coverage](https://s3.amazonaws.com/rs-code-coverage/wstunnel/cc_badge_master.svg)](https://gocover.io/github.com/rightscale/wstunnel)
- 1.0.7:
[![Build Status](https://travis-ci.org/rightscale/wstunnel.svg?branch=1.0.7)](https://travis-ci.org/rightscale/wstunnel)
[![Coverage](https://s3.amazonaws.com/rs-code-coverage/wstunnel/cc_badge_1.0.7.svg)](https://gocover.io/github.com/rightscale/wstunnel)

WStunnel creates an HTTPS tunnel that can connect servers sitting
behind an HTTP proxy and firewall to clients on the internet. It differs from many other projects
by handling many concurrent tunnels allowing a central client (or set of clients) to make requests
to many servers sitting behind firewalls. Each client/server pair are joined through a rendez-vous token.

At the application level the
situation is as follows, an HTTP client wants to make request to an HTTP server behind a
firewall and the ingress is blocked by a firewall:

```AsciiDoc
  HTTP-client ===> ||firewall|| ===> HTTP-server
```

The WStunnel app implements a tunnel through the firewall. The assumption is that the
WStunnel client app running on the HTTP-server box can make outbound HTTPS requests. In the end
there are 4 components running on 3 servers involved:

- the http-client application on a client box initiates HTTP requests
- the http-server application on a server box behind a firewall handles the HTTP requests
- the WStunnel server application on a 3rd box near the http-client intercepts the http-client's
  requests in order to tunnel them through (it acts as a surrogate "server" to the http-client)
- the WStunnel client application on the server box hands the http requests to the local
  http-server app (it acts as a "client" to the http-server)
The result looks something like this:

```AsciiDoc

HTTP-client ==>\                      /===> HTTP-server
                |                      |
                \----------------------/
            WStunsrv <===tunnel==== WStuncli
```

But this is not the full picture. Many WStunnel clients can connect to the same server and
many http-clients can make requests. The rendez-vous between these is made using secret
tokens that are registered by the WStunnel client. The steps are as follows:

- WStunnel client is initialized with a token, which typically is a sizeable random string,
  and the hostname of the WStunnel server to connect to
- WStunnel client connects to the WStunnel server using WSS or HTTPS and verifies the
  hostname-certificate match
- WStunnel client announces its token to the WStunnel server
- HTTP-client makes an HTTP request to WStunnel server with a std URI and a header
  containing the secret token
- WStunnel server forwards the request through the tunnel to WStunnel client
- WStunnel client receives the request and issues the request to the local server
- WStunnel client receives the HTTP response and forwards that back through the tunnel, where
  WStunnel server receives it and hands it back to HTTP-client on the still-open original
  HTTP request

In addition to the above functionality, wstunnel does some queuing in
order to handle situations where the tunnel is momentarily not open. However, during such
queing any HTTP connections to the HTTP-server/client remain open, i.e., they are not
made aware of the queueing happening.

The implementation of the actual tunnel is intended to support two methods (but only the
first is currently implemented).  
The preferred high performance method is websockets: the WStunnel client opens a secure
websockets connection to WStunnel server using the HTTP CONNECT proxy traversal connection
upgrade if necessary and the two ends use this connection as a persistent bi-directional
tunnel.  
The second (Not yet implemented!) lower performance method is to use HTTPS long-poll where the WStunnel client
makes requests to the server to shuffle data back and forth in the request and response
bodies of these requests.

## Getting Started

You will want to have 3 machines handy (although you could run everything on one machine to
try it out):

- `www.example.com` will be behind a firewall running a simple web site on port 80
- `wstun.example.com` will be outside the firewall running the tunnel server
- `client.example.com` will be outside the firewall wanting to make HTTP requests to
  `www.example.com` through the tunnel

### Download

Release branches are named '1.N.M' and a '1.N' package is created with each revision
as a form of 'latest'.  Download the latest [Linux binary](https://binaries.rightscale.com/rsbin/wstunnel/1.0/wstunnel-linux-amd64.tgz)
and extract the binary. To compile for OS-X or Linux ARM clone the github repo and run
`make depend; make` (this is not tested).

### Set-up tunnel server

On `wstun.example.com` start WStunnel server (I'll pick a port other than 80 for sake of example)

```bash
$ ./wstunnel srv -port 8080 &
2014/01/19 09:51:31 Listening on port 8080
$ curl http://localhost:8080/_health_check
WSTUNSRV RUNNING
$
```

### Start tunnel

On `www.example.com` verify that you can access the local web site:

```bash
$ curl http://localhost/some/web/page
<html> .......
```

Now set-up the tunnel:

```bash
$ ./wstunnel cli -tunnel ws://wstun.example.com:8080 -server http://localhost -token 'my_b!g_$secret!!'
2014/01/19 09:54:51 Opening ws://wstun.example.com/_tunnel
```

### Make a request through the tunnel

On `client.example.com` use curl to make a request to the web server running on `www.example.com`:

```bash

$ curl 'https://wstun.example.com:8080/_token/my_b!g_$secret!!/some/web/page'
<html> .......
$ curl '-HX-Token:my_b!g_$secret!!' https://wstun.example.com:8080/some/web/page
<html> .......
```

### Targeting multiple web servers

The above example tells WStunnel client to only forward requests to `http://localhost`. It is possible to allow the wstunnel to target multiple hosts too. For this purpose the original HTTP client must pass an X-Host header to name the host and WStunnel client must be configured with a regexp that limits the destination web server hostnames it allows. For example, to allow access to `*.some.example.com` over https use:

- `wstunnel cli -regexp 'https://.*\.some\.example\.com' -server https://default.some.example.com ...`
- `curl '-HX-Host: https://www.some.example.com'`

Or to allow access to www.example.com and blog.example.com over http you might use:

- `wstunnel cli -regexp 'http://(www\.example\.com|blog\.example\.com)' -server http://www.example.com ...`
- `curl '-HX-Host: http://blog.example.com'`

Note the use of -server and -regexp, this is because the server named in -server is used when there is no X-Host header. The host in the -server option does not have to match the regexp but it is recommended for it match.

### Using a Proxy

WStunnel client may use a proxy as long as that proxy supports HTTPS CONNECT. Basic authentication may be used if the username and password are embedded in the url. For example, `-proxy http://myuser:mypass@proxy-server.com:3128`. In addition, the command line client will also respect the https_proxy/http_proxy environment variables if they're set. As websocket connections are very long lived, please set read timeouts on your proxy as high as possible.

### Using Secure Web Sockets (SSL)

WStunnel does not support SSL natively (although that would not be a big change). The recommended
approach for using WSS (web sockets through SSL) is to use nginx, which uses the well-hardened
openssl library, whereas WStunnel would be using the non-hardened Go SSL implementation.
In order to connect to a secure tunnel server from WStunnel client use the `wss` URL scheme, e.g.
`wss://wstun.example.com`.
Here is a sample nginx configuration:

````nginx

server {
  listen       443;
  server_name  wstunnel.test.rightscale.com;

  ssl_certificate        <path to crt>;
  ssl_certificate_key    <path to key>;

  # needed for HTTPS
  proxy_set_header X-Forwarded-Proto https;
  proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
  proxy_set_header Host $http_host;
  proxy_set_header X-Forwarded-Host $host;
  proxy_redirect off;
  proxy_max_temp_file_size 0;

  #configure ssl
  ssl on;
  ssl_protocols SSLv3 TLSv1;
  ssl_ciphers HIGH:!ADH;
  ssl_prefer_server_ciphers on; # don't trust the client
  # caches 10 MB of SSL sessions in memory, faster than OpenSSL's cache:
  ssl_session_cache shared:SSL:10m;
  # cache the SSL sessions for 5 minutes, just as long as today's browsers
  ssl_session_timeout 5m;

  location / {
    root /mnt/nginx;

    proxy_redirect     off;
    proxy_http_version 1.1;

    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "Upgrade";
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-For    $proxy_add_x_forwarded_for;
    proxy_buffering off;

    proxy_pass http://127.0.0.1:8080;   # assume wstunsrv runs on port 8080
  }
}
````

### Reading wstunnel server logs

Sample:

```text
Apr  4 17:40:29 srv1 wstunsrv[7808]: INFO HTTP RCV      pkg=WStunsrv token=tech_x... id=19 verb=GET url=/ addr="10.210.2.11, 53.5.22.247" x-host= try=
Apr  4 17:40:29 srv1 wstunsrv[7808]: INFO WS   SND      pkg=WStunsrv token=tech_x... id=19 info="GET /"
Apr  4 17:40:29 srv1 wstunsrv[7808]: INFO WS   RCV      token=tech_x... id=19 ws=0xc20a416d20 len=393
Apr  4 17:40:29 srv1 wstunsrv[7808]: INFO HTTP RET      pkg=WStunsrv token=tech_x... id=19 status=401
```

The first line says that wstunsrv received an HTTP request to be tunneled and assigned it id 19.
The second line says that wstunsrv sent the request onto the appropriate websocket ("WS") to wstuncli.
The third line says that it has received a response to request 19 over the websocket.
The fourth line says that wstunsrv sent an HTTP response back to request 19, and that the status is a 401. What may not be obvious is that because it went round-trip to wstuncli the status code comes from wstuncli.

## Release instructions

1. Run the following bash commands.

```bash
make depend
git tag -a $VERSION
git push --tags
```

1. Create a branch for the changelog.
1. Create the changelog:

```bash
git-chglog -o CHANGELOG.md
git add CHANGELOG.md
```

1. Update Readme to reflect new `$VERSION`.
1. Commit and push README and CHANGLOG Changes.
