WStunnel - Web Sockets Tunnel
=============================

This pair of client and server apps implement an HTTPS tunnel that can connect servers sitting
behind an HTTP proxy and firewall to clients on the internet.  At the application level the
situation is as follows, an HTTP client wants to make request to an HTTP server behind a
firewall and the ingress is blocked by a firewall:

    HTTP-client ===> ||firewall|| ===> HTTP-server

The WStunnel app pair implements a tunnel through the firewall. The assumption is that the
WStun-client app running on the HTTP-server box can make outbound HTTPS requests. In the end
there are 4 components running on 3 servers involved:
 - the http-client application on a client box initiates HTTP requests
 - the http-server application on a server box handles the HTTP requests
 - the wstun-server application on a 3rd box near the http-client intercepts the http-client's
   requests in order to tunnel them through
 - the wstun-client application on the server box hands the http requests to the local
   http-server app
The result looks something like this:

    HTTP-client ===\                      /===> HTTP-server
                   |                      |
                   \----------------------/
           WStun-server <===tunnel==== WStun-client

But htis is not the full picture. Many WStun-clients can connect to the same server and
many http-clients can make requests. The rendez-vous between these is made using secret
tokens that are registered by the WStun-client. The steps are as follows:
 - WStun-client is initialized with a token, which typically is a sizeable random string,
   and the hostname of the WStun-server to connect to
 - WStun-client connects to the WStun-server using WSS or HTTPS and verifies the
   hostname-certificate match
 - WStun-client announces its token to the WStun-server
 - HTTP-client makes an HTTP request to WStun-server with a std URI and a Host header
   containing the secret token
 - WStun-server forwards the request through the tunnel to WStun-client
 - WStun-client receives the request and issues the request to localhost:80
 - WStun-client receives the HTTP reqponse and forwards that back through the tunnel, where
   WStun-server receives it and hands it back to HTTP-client on the still-open original
   HTTP request

In addition to the above functionality, WStun-server and WStun-client do some queuing in
order to handle situations where the tunnel is momentarily not open. However, during such
queing any HTTP connections to the HTTP-server/client remain open, i.e., they are not
made aware of the queueing happening.

The implementation of the actual tunnel supports two methods.
The preferred high performance method is websockets: the WStun-client opens a secure
websockets connection to WStun-server using the HTTP CONNECT proxy traversal connection
upgrade if necessary and the two ends use this connection as a persistent bi-directional
tunnel.
The second lower performance method is to use HTTPS long-poll where the WStun-client
makes requests to the server to shuffle data back and forth in the request and response
bodies of these requests.

