WStunnel - Web Sockets Tunnel
=============================

This pair of client and server apps implement an HTTPS tunnel that can connect servers sitting
behind an HTTP proxy and firewall to clients on the internet. It differs from many other projects
by handling many concurrent tunnels allowing a central client (or set of clients) to make requests
to many servers sitting behind firewalls. Each client/server pair are joined through a rendez-vous token.

At the application level the
situation is as follows, an HTTP client wants to make request to an HTTP server behind a
firewall and the ingress is blocked by a firewall:

    HTTP-client ===> ||firewall|| ===> HTTP-server

The WStunnel app pair implements a tunnel through the firewall. The assumption is that the
WStuncli app running on the HTTP-server box can make outbound HTTPS requests. In the end
there are 4 components running on 3 servers involved:
 - the http-client application on a client box initiates HTTP requests
 - the http-server application on a server box handles the HTTP requests
 - the wstunsrv application on a 3rd box near the http-client intercepts the http-client's
   requests in order to tunnel them through
 - the wstuncli application on the server box hands the http requests to the local
   http-server app
The result looks something like this:

````
    HTTP-client ==>\                      /===> HTTP-server
                   |                      |
                   \----------------------/
               WStunsrv <===tunnel==== WStuncli
````

But this is not the full picture. Many wstuncli can connect to the same server and
many http-clients can make requests. The rendez-vous between these is made using secret
tokens that are registered by the wstuncli. The steps are as follows:
 - wstuncli is initialized with a token, which typically is a sizeable random string,
   and the hostname of the wstunsrv to connect to
 - wstuncli connects to the wstunsrv using WSS or HTTPS and verifies the
   hostname-certificate match
 - wstuncli announces its token to the wstunsrv
 - HTTP-client makes an HTTP request to wstunsrv with a std URI and a Host header
   containing the secret token
 - wstunsrv forwards the request through the tunnel to wstuncli
 - wstuncli receives the request and issues the request to localhost:80
 - wstuncli receives the HTTP reqponse and forwards that back through the tunnel, where
   wstunsrv receives it and hands it back to HTTP-client on the still-open original
   HTTP request

In addition to the above functionality, wstunsrv and wstuncli do some queuing in
order to handle situations where the tunnel is momentarily not open. However, during such
queing any HTTP connections to the HTTP-server/client remain open, i.e., they are not
made aware of the queueing happening.

The implementation of the actual tunnel is intended to support two methods (but only the
first is currently implemented).  
The preferred high performance method is websockets: the wstuncli opens a secure
websockets connection to wstunsrv using the HTTP CONNECT proxy traversal connection
upgrade if necessary and the two ends use this connection as a persistent bi-directional
tunnel.  
The second (Not yet implemented!) lower performance method is to use HTTPS long-poll where the wstuncli
makes requests to the server to shuffle data back and forth in the request and response
bodies of these requests.

Getting Started
---------------

You will want to have 3 machines handy (although you could run everything on one machine to
try it out):
 - `www.example.com` will be behind a firewall running a simple web site on port 80
 - `wstun.example.com` will be outside the firewall running the tunnel server
 - `client.example.com` will be outside the firewall wanting to make HTTP requests to
   `www.example.com` through the tunnel

If yo don't have it locally, you will need to fetch Gorilla Web Sockets: `go get github.com/gorilla/websocket`

.h3 Set-up tunnel server

On `wstun.example.com` start wstunsrv (I'll pick a port other than 80 for sake of example)

    $ go build wstunsrv.go ws.go
    $ ./wstunsrv -port 8080 &
    2014/01/19 09:51:31 Listening on port 8080
    $ curl https://localhost:8080/_health_check
    WSTUNSRV RUNNING
    $ 

.h3 Start tunnel

On `www.example.com` verify that you can access the local web site:

    $ curl http://localhost/some/web/page
    <html> .......

Now set-up the tunnel:

    $ go build wstuncli.go
    $ ./wstuncli -tunnel ws:/wstun.example.com:8080 -server http://localhost -token 'my_b!g_$secret'
    2014/01/19 09:54:51 Opening ws://wstun.example.com/_tunnel

.h3 Make a request through the tunnel

On `client.example.com` use curl to make a request to the web server running on `www.example.com`:

    $ curl 'https://wstun.example.com:8080/_token/my_b!g_$secret/some/web/page'
    <html> .......
    $ curl '-HX-Token:my_b!g_$secret' https://wstun.example.com:8080/some/web/page
    <html> .......

.h3 Using Secure Web Sockets (SSL)

Wstunsrv does not support SSL natively (although that would not be a big change). The recommended
approach for using WSS (web sockets through SSL) is to use nginx, which uses the well-hardened
openssl library, whereas wstunsrv would be using the non-hardened Go SSL implementation.
In order to connect to a secure tunnel server from wstuncli use the `wss` URL scheme, e.g.
`wss://wstun.example.com`.
Here is a sample nginx configuration:

````
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
