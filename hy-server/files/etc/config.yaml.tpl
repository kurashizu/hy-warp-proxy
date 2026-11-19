listen: :443
tls:
  cert: /etc/hysteria/server.crt
  key: /etc/hysteria/server.key
auth:
  type: password
  password: @PASSWORD@
quic:
  disablePathMTUDiscovery: true

# all egress goes out through the "warp" WireGuard interface (fail closed)
outbounds:
  - name: warp
    type: direct
    direct:
      mode: auto
      bindDevice: warp

# server-side DNS resolution must also go through warp (routes added by warp.conf PostUp)
resolver:
  type: udp
  udp:
    addr: 1.1.1.1:53
    timeout: 4s

# probes see a plain nginx 404
masquerade:
  type: string
  string:
    content: "<html>\r\n<head><title>404 Not Found</title></head>\r\n<body>\r\n<center><h1>404 Not Found</h1></center>\r\n<hr><center>nginx</center>\r\n</body>\r\n</html>\r\n"
    headers:
      content-type: text/html
      server: nginx
    statusCode: 404

acl:
  inline:
@ACL@
    - warp(all)
