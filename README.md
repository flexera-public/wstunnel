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

