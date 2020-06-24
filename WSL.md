# Running WSTunnel Client in Ubuntu Windows Subsystem for Linux

## Instructions

```shell
  curl -o /tmp/wstunnel-linux-amd64.tgz https://binaries.rightscale.com/rsbin/wstunnel/master/wstunnel-linux-amd64.tgz
  cd /tmp
  tar -xvzf wstunnel-linux-amd64.tgz
  sudo cp wstunnel/wstunnel /usr/local/bin/wstunnel
  sudo cp wstunnel/init/wstuncli.default /etc/default/wstuncli
  # Update /etc/default/wstuncli with your settings
  sudo cp wstunnel/init/wstuncli.init /etc/init.d/wstuncli
  chmod +x /etc/init.d/wstuncli
  update-rc.d wstuncli defaults
  service wstuncli start
```
