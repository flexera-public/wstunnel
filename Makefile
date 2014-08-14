all: wstuncli wstunsrv
wstuncli: wstuncli.go go-version
	go build wstuncli.go
wstunsrv: wstunsrv.go ws.go robowhois.go go-version
	go build wstunsrv.go ws.go robowhois.go
.s3cfg:
	echo "Sorry, you need .s3cfg set-up before upload"
	false
go-version:
	@if ! go version | egrep -q "go1.3"; then echo "You must use go 1.3"; false; fi
commit-check:
	@if ! git status | egrep -q "working directory clean"; then echo "Please commit first"; false; fi
s3=rightscale-vscale/wstunnel/
sha=`git log | head -1 | cut -c 8-13`
upload: all .s3cfg commit-check
	cp wstuncli wstuncli-${sha}
	cp wstunsrv wstunsrv-${sha}
	echo s3cmd -P -c ./.s3cfg --force put wstuncli wstuncli-${sha} wstunsrv wstunsrv-${sha} s3://${s3}
