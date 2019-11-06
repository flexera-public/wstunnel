# ChangeLog

## [1.0.7](https://github.com/rightscale/wstunnel/compare/1.0.6...1.0.7)### Add

* Add Support for Wstuncli port range ([#35](https://github.com/rightscale/wstunnel/issues/35))
* Add support for binding to the specific host ([#23](https://github.com/rightscale/wstunnel/issues/23)) ([#36](https://github.com/rightscale/wstunnel/issues/36))
* Add windows support.
* Add ginkgo binary as a dependency
* Add comments to readme

### Build

* Build and Upload, fixes [#34](https://github.com/rightscale/wstunnel/issues/34) ([#38](https://github.com/rightscale/wstunnel/issues/38))

### Correct

* Correct protocol for tunserv

### Fix

* Fix Travis CI harder
* Fix Makefile to work under Windows/Cygwin
* Fix typo in README
* Fix minor documentation bugs

### Make

* Make GoLint Happy ([#37](https://github.com/rightscale/wstunnel/issues/37))
* Make Travis CI happy as well as the Windows build
* Make version.go editing work for Travis

### Merge

* Merge branch 'master' into patch-1

### Removing

* Removing the reference to HTTPS not supported.

### Switch

* Switch from Godep to dep

### Updated

* updated readme for latest version

### Pull Requests

* Merge pull request [#21](https://github.com/rightscale/wstunnel/issues/21) from chribben/patch-1
* Merge pull request [#29](https://github.com/rightscale/wstunnel/issues/29) from rightscale/ZD156280-1
* Merge pull request [#28](https://github.com/rightscale/wstunnel/issues/28) from rightscale/ph-update-dependencies
* Merge pull request [#25](https://github.com/rightscale/wstunnel/issues/25) from flaccid/patch-1

## [1.0.6](https://github.com/rightscale/wstunnel/compare/1.0.5...1.0.6)### Add

* add tests to large requests and responses

### Dump

* dump req/resp in case of error, try 2
* dump req/resp in case of error

### Fix

* fix tests not to print to stderr
* fix race condition when reading long messages from websocket in wstuncli
* fix new-websocket race in wstuncli

### Try

* try to fix test race condition [#2](https://github.com/rightscale/wstunnel/issues/2)
* try to fix test race condition

### Tweak

* tweak debug output

## [1.0.5](https://github.com/rightscale/wstunnel/compare/1.0.4...1.0.5)### Added

* added logging to track ws connect/disconnect

### Fix

* fix error propagation in wsReader
* fix request timeout calculation
* fix panics when pings fail

### Logging

* logging tweak

### Try

* try to fix random test failures

### Tweak

* tweak readme

### Pull Requests

* Merge pull request [#16](https://github.com/rightscale/wstunnel/issues/16) from rightscale/IV-2077_proxy

## [1.0.4](https://github.com/rightscale/wstunnel/compare/1.0.3...1.0.4)### Attempt

* attempt to test websocket reconnection

### Fix

* fix FD leak in wstuncli; add test

### Update

* update readme

## [1.0.3](https://github.com/rightscale/wstunnel/compare/1.0.2...1.0.3)### Fix

* fix non-remove requests on tunnel abort; fix non-reopening of WS at client

## [1.0.2](https://github.com/rightscale/wstunnel/compare/1.0.1...1.0.2)### Added

* added statusfile option

### Readme

* readme fix

### Tweak

* tweak readme

## [1.0.1](https://github.com/rightscale/wstunnel/compare/1.0.0...1.0.1)### Add

* add more debug info to x-host match failure

### Better

* better non-existent tunnel handling

### Change

* change syslog to LogFormatter, which isn't great either

### Fix

* fix recursive code coverage

### Makefile

* makefile fix 4
* makefile fix 3
* makefile fix 2
* makefile fix

### Support

* support syslog on client; improve syslog log format

### Updated

* updated readme

## 1.0.0### Acu152721

* acu152721 use exec su, since setuid is forced on pre-start
* acu152721 Have upstart use the www-data user

### Add

* add godeps

### Added

* added upstart config files to tgz
* added support for internal servers
* added more tests
* added log15 dependency, added missing test file
* added upload to S3 to makefile, removed binaries from git
* added whois lookup to tunnel endpoint IP addresses
* added stats request; added explicit GC
* added health check; fixed wstunsrv upstart config
* added upstart conf for wstunsrv and minor edit to wstuncli.conf
* added ubuntu upstart and config, license, and binaries

### Created

* created tunnel package

### Delete

* Delete README.md

### First

* first passing test
* first working version

### Fix

* fix cmdline parsing
* fix async AbortConnection
* fix test suite and coverprofile
* fix host header
* fix x-host error responses; add test cases
* fix code coverage badge
* fix travis.yml
* fix go vet booboo
* fix govet complaints
* fix version
* fix readme
* fix some more logging
* fix some more logging
* fix whois regexp
* fix deadlock in stats handler; add reverse DNS lookup for tunnels
* fix problems with chunked encoding and other transfer encodings
* fix problems with chunked encoding and other transfer encodings
* fix readme headings

### Fixed

* fixed FD leak; added syslog flag; added tunnel keys; sanitize tokens from log
* fixed README instructions

### Godep

* godep hell

### Improved

* improved test for host header
* improved logging; removed read/write timeouts due to bug

### Initial

* Initial commit

### Made

* made x-host header work

### Makefile

* makefile tweak
* makefile tweak
* makefile tweak
* makefile tweak

### Merge

* merge client & server into a single main

### Merge

* Merge branch 'master' of github.com:rightscale/wstunnel
* Merge branch 'master' of github.com:rightscale/wstunnel
* Merge branch 'master' of github.com:rightscale/wstunnel

### More

* More readme changes

### More

* more debugging for wstuncli errors; inadvertant gofmt

### Remove

* remove wstunsrv binary again
* remove read/write timeouts on socket; fix logging typo

### Removed

* Removed logfile and added syslog options

### Removed

* removed default for -server option

### Restrict

* restrict _stats to localhost; improve whois parsing; add minimum token length; print error on failed tunnel connect

### Run

* run gofmt

### Squash

* squash file descriptor leak in wstunsrv

### Start

* start on runlevel 2 as well

### Started

* started to update readme

### Timeout

* timeout tweaks, recompile to get go1.3 timeout fixes

### Travis

* travis support

### Update

* Update README.md

### Updated

* updated README

### Version

* version and logging tweaks

### Pull Requests

* Merge pull request [#12](https://github.com/rightscale/wstunnel/issues/12) from rightscale/multihost
* Merge pull request [#4](https://github.com/rightscale/wstunnel/issues/4) from rightscale/acu153476_use_syslog
* Merge pull request [#2](https://github.com/rightscale/wstunnel/issues/2) from rightscale/acu152721_run_as_www_data_user

