all: wstuncli wstunsrv
wstuncli: wstuncli.go
	go build wstuncli.go
wstunsrv: wstunsrv.go ws.go robowhois.go
	go build wstunsrv.go ws.go robowhois.go

